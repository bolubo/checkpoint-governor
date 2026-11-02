// Package supervisor runs one reconcile loop per unit over one work queue.
//
// It is the controller-manager of this repository: a registry of units, a queue of keys to
// reconcile, and a floor under the queue. It owns nothing a loop needs — each loop owns its
// directory's lease, its attempts and its plan — so "no shared mutable state" is not a goal here,
// it is the shape: the only things the supervisor keeps are the queue, the timer and the map of
// loops. A unit that is added or that changes generation is queued and reconciled; a unit that
// disappears is closed.
package supervisor

import (
	"sort"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/loop"
	"github.com/bolubo/checkpoint-governor/internal/notify"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce"
	"github.com/bolubo/checkpoint-governor/internal/queue"
	"github.com/bolubo/checkpoint-governor/internal/unit"
)

// Config is what every unit shares. Anything that differs per unit comes from the member set.
type Config struct {
	Policy      policy.Policy
	Bounds      policy.Bounds
	Now         func() time.Time
	BlindAfter  int
	VerifyEvery time.Duration

	// ProbesFor builds the condition probes for one unit.
	//
	// It replaced a single []Probe when the supervisor learned to hold units from more than one
	// cluster: the world probes are bound to a (worldDir, cluster) pair, and one set shared across
	// units would read the wrong cluster for all but one of them. A unit whose Cluster is empty gets
	// the single-cluster probes, which is the special case, not a separate code path.
	ProbesFor func(unit.Unit) []produce.Probe

	// PolicyFor builds the policy for one unit, for the same reason: a deployment may add conditions
	// from disk (conditions.d, decision 0024), and those rows are per directory. Nil means Policy for
	// every unit, which is a deployment with nothing added.
	PolicyFor func(unit.Unit) policy.Policy

	// Window is every unit's `with-us`: the longest a checkpoint may stay stale while we are alive.
	// Zero means off (exceptions only). It is one number for the whole supervisor because it is the
	// deployment's policy, not a per-directory accident.
	Window time.Duration

	// Holder identifies this process for each directory's lease. Every unit gets its own directory,
	// so one holder name is enough; the lease is per directory, never per supervisor.
	Holder string

	// Notify is told about every plan that changed, for every unit. The loop fills in the unit's own
	// name and cluster, so one hook serves the whole supervisor; a failure is recorded per unit and
	// never stops a tick.
	Notify func(notify.Event) error

	Queue  queue.Options
	Resync time.Duration

	// VolumeLimit is how many writes may run at once on one volume. Zero means one: a volume with no
	// limit is a volume that will be asked to do everything at the worst moment. Several units may
	// share one volume, and no unit can see the others, so the limit is enforced here.
	VolumeLimit int

	// Fault is the crash-injection hook the loops share: a test returns an error at a named point.
	// Production leaves it nil.
	Fault func(point string) error
}

// Supervisor owns the loops and their queue.
type Supervisor struct {
	cfg     Config
	reg     *unit.Registry
	loops   map[string]*loop.Loop
	units   map[string]unit.Unit
	volumes *unit.Gate
	queue   *queue.Queue
	resync  *queue.Resync
}

func Open(cfg Config) *Supervisor {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Supervisor{
		cfg:     cfg,
		reg:     unit.NewRegistry(),
		loops:   map[string]*loop.Loop{},
		units:   map[string]unit.Unit{},
		volumes: unit.NewGate(cfg.VolumeLimit),
		queue:   queue.New(cfg.Queue),
		resync:  queue.NewResync(cfg.Resync),
	}
}

// Sync replaces the writing set with the one given.
//
// A unit that appears is opened and queued ("it exists" is a change). A unit whose generation moved
// is queued and told to advance; the loop applies it at the start of its next round. A unit that
// disappears is closed, which releases its lease instead of making the next controller wait for it
// to expire.
func (s *Supervisor) Sync(members []unit.Member) error {
	seen := map[string]bool{}
	for _, u := range s.reg.Sync(members) {
		seen[u.Dir] = true
		l, ok := s.loops[u.Dir]
		if !ok {
			fresh, err := loop.Open(loop.Config{
				Dir:         u.Dir,
				UnitID:      u.ID,
				Bounds:      s.cfg.Bounds,
				Policy:      policyFor(s.cfg, u),
				Now:         s.cfg.Now,
				Probes:      probesFor(s.cfg.ProbesFor, u),
				BlindAfter:  s.cfg.BlindAfter,
				Window:      s.cfg.Window,
				VerifyEvery: s.cfg.VerifyEvery,
				Holder:      s.cfg.Holder,
				Fault:       s.cfg.Fault,
				Volume:      volumeKey(u),
				Volumes:     s.volumes,
				Cluster:     u.Cluster,
				Notify:      s.cfg.Notify,
			})
			if err != nil {
				return err
			}
			s.loops[u.Dir] = fresh
			s.queue.Add(u.Dir)
		} else if u.Generation > l.Ledger().Generation {
			l.AdvanceGeneration(u.Generation)
			s.queue.Add(u.Dir)
		}
		s.units[u.Dir] = u
	}
	for dir, l := range s.loops {
		if seen[dir] {
			continue
		}
		_ = l.Close()
		delete(s.loops, dir)
		delete(s.units, dir)
	}
	return nil
}

// Enqueue marks one unit dirty, which is what an event from outside does.
func (s *Supervisor) Enqueue(dir string) { s.queue.Add(dir) }

// probesFor calls the builder when there is one. A nil builder is a supervisor whose units need no
// probes, which is what the loop's own tests use.
func probesFor(build func(unit.Unit) []produce.Probe, u unit.Unit) []produce.Probe {
	if build == nil {
		return nil
	}
	return build(u)
}

// policyFor prefers the per-unit builder and falls back to the shared policy.
func policyFor(cfg Config, u unit.Unit) policy.Policy {
	if cfg.PolicyFor != nil {
		if p := cfg.PolicyFor(u); p != nil {
			return p
		}
	}
	return cfg.Policy
}

// volumeKey is the slot a unit's write competes for. It carries the cluster name because two
// clusters both mounting `/ckpt` are two volumes, not one: without the prefix the second cluster's
// write would be refused by a unit it cannot even see.
func volumeKey(u unit.Unit) string {
	if u.Cluster == "" {
		return u.Volume
	}
	return u.Cluster + "|" + u.Volume
}

// Tick reconciles what is queued, plus what the floor finds.
//
// The floor runs first, so a unit whose event was never delivered is reconciled in the same tick,
// one interval late rather than never. A key that fails goes back with a growing pause; the failure
// is returned, because the caller is the one that has to say it out loud.
func (s *Supervisor) Tick() (map[string]loop.Step, error) {
	now := s.cfg.Now()
	out := make(map[string]loop.Step, len(s.loops))
	// Nothing outside has to happen for a unit to need a round: a write in flight has to be settled,
	// a timer expires, or the policy asked to be consulted again. Every unit is asked once, in
	// memory, so a write that is already due costs one Add.
	for dir, l := range s.loops {
		if l.NeedsAttention(now) {
			s.queue.Add(dir)
		}
	}
	s.resync.Sweep(s.queue, s.Dirs(), now)
	for {
		key := s.queue.Get(now)
		if key == "" {
			return out, nil
		}
		l, ok := s.loops[key]
		if !ok {
			s.queue.Done(key)
			continue
		}
		st, err := l.Reconcile()
		if err != nil {
			s.queue.Fail(key, now)
			return out, err
		}
		s.queue.Done(key)
		out[key] = st
	}
}

// Dirs is the units this supervisor knows, sorted by directory.
func (s *Supervisor) Dirs() []string {
	out := make([]string, 0, len(s.loops))
	for dir := range s.loops {
		out = append(out, dir)
	}
	// A stable order matters for the resync sweep: two processes started with the same set must not
	// disagree about what they did.
	sortStrings(out)
	return out
}

// Loop exposes one unit's loop for tests and for a caller that wants to render it.
func (s *Supervisor) Loop(dir string) *loop.Loop { return s.loops[dir] }

// Units returns the units with their cluster and world coordinates, sorted by directory. A rollup
// needs the cluster to say how many it holds; the loop itself never does.
func (s *Supervisor) Units() []unit.Unit {
	out := make([]unit.Unit, 0, len(s.units))
	for _, u := range s.units {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

// Queue exposes the queue's numbers.
func (s *Supervisor) Queue() queue.Stats { return s.queue.Stats() }

// InUse is how many writes the volume is running now, for the view.
func (s *Supervisor) InUse(volume string) int { return s.volumes.InUse(volume) }

// Close releases every lease.
func (s *Supervisor) Close() error {
	var first error
	for _, l := range s.loops {
		if err := l.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.loops = map[string]*loop.Loop{}
	return first
}

// sortStrings is a small insertion sort: the package has no other use for sorting, and a stable
// order is only needed for readability here.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
