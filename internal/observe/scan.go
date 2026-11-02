// Package observe reads a checkpoint directory into the snapshot the policy decides from.
//
// It reads the directory and the file system, and nothing else: no clock (Now is a field), no
// cluster, no network. A measurement it cannot take is written as the unknown value the contract
// names, never as a plausible number — the algorithm's guarantee is only as honest as this package.
package observe

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/assemble"
	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Request is everything the caller knows that the directory itself does not carry.
//
// The pointer fields are the job's own reports: nil means "not written", which is different from
// zero (a job at step 0, or a write 0% done, are real facts). The ledger fields are the caller's
// memory across restarts; this package keeps none of its own.
type Request struct {
	Dir        string
	Now        time.Time
	UnitID     string
	Generation int64

	// Step and Progress come from the job's files (design, "What the training job writes").
	Step     *int64
	Progress *float64

	// Seen maps a path relative to Dir to the first moment the controller saw it. It is what makes
	// a write's cost measurable on the controller's clock instead of the trainer's.
	Seen map[string]time.Time

	// Changed maps a directory name to the last moment the controller saw it change. It is what
	// "quiet" is measured on when the caller keeps it; without it, the directory's own modification
	// time is used, which is the same clock on one machine and a different one across a network
	// share.
	Changed map[string]time.Time

	// Samples and counters are carried by the caller; the event loop appends to them.
	WriteCostSamples  []float64
	WriteSecondsTotal *float64
	Failures          int

	// Verified and Mismatched are the verifier's results, keyed by step. They are inputs because the
	// verifier is a separate, sampled loop: reading every byte costs more than writing it, so it can
	// never be part of a scan.
	Verified   map[int64]bool
	Mismatched map[int64]bool

	// Evidence is what the declared capabilities produced. Absent capabilities are not
	// here; the algorithm names them in Missing.
	Evidence map[string]policy.Observation

	// Stat is injectable so that a test can present a volume with no statistics. A nil Stat uses
	// the real file system.
	Stat func(dir string) (free, total int64, err error)
}

// Report is what the caller logs and counts. Nothing here reaches the algorithm: these are facts
// about why a directory was not a checkpoint, not measurements.
type Report struct {
	Unreadable []string
	InFlight   string
	Notes      []string
	// Failures are structured, so that the caller can count them and name them to a person. A note
	// is prose and must never be parsed.
	Failures []Failure
	// ChangedAt is the newest modification of each step directory, for the caller that keeps its
	// own clock: it compares this with the last value it saw and records the moment it moved.
	ChangedAt map[string]time.Time
	// Reads and Stats are the system calls this scan spent, counted where they happen. They are the
	// only honest basis for the governor's own overhead.
	Reads int
	Stats int
}

// Failure codes are the implementation's vocabulary. The job never sees them.
const (
	FailAbandoned  = "write_abandoned"
	FailShardShort = "shard_short"
	FailShardGone  = "shard_missing"
	FailManifest   = "manifest_invalid"
	FailStepName   = "step_mismatch"
	// FailSealStale means a shard changed after the manifest was written: the directory was sealed
	// once and rewritten afterwards, so what is on disk is neither the old checkpoint nor the new one.
	FailSealStale = "seal_stale"
)

// Failure is one reason a directory is not a checkpoint.
type Failure struct {
	Dir    string
	Code   string
	Detail string
}

// Scan reads one checkpoint directory.
//
// It returns an error only when the directory itself cannot be listed. In that case the caller must
// not decide at all: a snapshot with no copies would say "there is no checkpoint, write one", and
// writing into a directory we cannot read is not a decision, it is a guess.
// Scanner reads the same directory over and over, remembering the manifests it has parsed.
//
// The controller owns one; the package-level Scan is for one-off reads and tests.
type Scanner struct {
	cache *manifestCache
}

func NewScanner() *Scanner { return &Scanner{cache: newManifestCache()} }

func (s *Scanner) Scan(req Request) (policy.Snapshot, Report, error) {
	return scan(req, s.cache)
}

// Scan reads one directory and forgets everything afterwards.
func Scan(req Request) (policy.Snapshot, Report, error) { return scan(req, nil) }

func scan(req Request, cache *manifestCache) (policy.Snapshot, Report, error) {
	// ReadDir is one enumeration; every directory below adds its own stats and reads.
	rep := Report{ChangedAt: map[string]time.Time{}, Stats: 1}
	entries, err := os.ReadDir(req.Dir)
	if err != nil {
		return policy.Snapshot{}, rep, err
	}

	window := abandonWindow(req.WriteCostSamples)

	copies := make([]assemble.Copy, 0, len(entries))
	alive := make(map[string]bool, len(entries))
	var unsealed []stepDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := parseStepName(e.Name()); !ok {
			continue // .ckpt-governor and anything else that is not a checkpoint
		}
		alive[filepath.Join(req.Dir, e.Name(), ManifestName)] = true
		d := readStepDir(req.Dir, e.Name(), cache)
		rep.ChangedAt[d.name] = d.changedAt
		rep.Reads += d.reads
		rep.Stats += d.stats
		if d.sealed {
			state := assemble.Sealed
			switch {
			case req.Mismatched[d.step]:
				state = assemble.Broken
			case req.Verified[d.step]:
				state = assemble.Verified
			}
			copies = append(copies, assemble.Copy{
				Step: d.step, Bytes: d.bytes, State: state, CreatedAt: firstSeen(req.Seen, e.Name(), d.changedAt),
			})
			continue
		}
		// A manifest that does not match its shards is broken now, whatever the clock says: the
		// write said it was finished and it is not. Only a directory with **no** manifest can be a
		// write in progress.
		if d.code != "" {
			rep.Notes = append(rep.Notes, d.name+": "+d.reason)
			rep.Failures = append(rep.Failures, Failure{Dir: d.name, Code: d.code, Detail: d.reason})
			copies = append(copies, assemble.Copy{
				Step: d.step, State: assemble.Broken, CreatedAt: firstSeen(req.Seen, e.Name(), d.changedAt),
			})
			continue
		}

		// A write that has stopped moving is dead, however long it has been open (decision 0004).
		// It becomes a broken copy, which is the state the policy answers with repair.
		if quiet := quietFor(d, req); quiet >= window {
			detail := "quiet for " + quiet.Round(time.Second).String()
			rep.Notes = append(rep.Notes, d.name+": "+FailAbandoned+" ("+detail+")")
			rep.Failures = append(rep.Failures, Failure{Dir: d.name, Code: FailAbandoned, Detail: detail})
			copies = append(copies, assemble.Copy{
				Step: d.step, State: assemble.Broken, CreatedAt: firstSeen(req.Seen, e.Name(), d.changedAt),
			})
			continue
		}
		unsealed = append(unsealed, d)
		rep.Notes = append(rep.Notes, d.name+": "+d.reason)
		copies = append(copies, assemble.Copy{
			Step: d.step, State: assemble.Unverified, CreatedAt: firstSeen(req.Seen, e.Name(), d.changedAt),
		})
	}
	cache.prune(alive)
	sort.Slice(copies, func(i, j int) bool { return copies[i].Step < copies[j].Step })

	newest := newestSealed(copies)

	var checkpointBytes int64
	var lastSeal time.Time
	if newest != nil {
		checkpointBytes = newest.Bytes
		dir := dirName(newest.Step)
		lastSeal = seenAt(req.Seen, dir, ManifestName)
		if lastSeal.IsZero() {
			// First round: nothing has been recorded yet, and the manifest's own modification time
			// is the only trace of when the seal happened.
			lastSeal = rep.ChangedAt[dir]
		}
	}

	// A directory without a manifest that was first seen recently is the write in flight. "Recently"
	// is three writes or a minute, whichever is longer: a write that takes longer than that is not
	// slow, it is stuck, and it must become a failure instead of an eternal hold.
	inFlight, started := inFlightOf(unsealed, req.Seen, req.WriteCostSamples, req.Now)
	if inFlight != "" {
		rep.InFlight = inFlight
	}

	step, stepSource := stepOf(req.Step, newest)

	progress := -1.0
	if req.Progress != nil {
		progress = *req.Progress
	}

	free := int64(-1)
	stat := req.Stat
	if stat == nil {
		stat = diskUsage
	}
	// Free space is the only number the contract keeps from the volume: the headroom rule is about
	// what is left, and a total nobody reads is a measurement taken for nothing.
	if f, _, err := stat(req.Dir); err == nil {
		free = f
	} else {
		rep.Unreadable = append(rep.Unreadable, "volume statistics: "+err.Error())
	}

	// Only what the risk core and the executor actually read (policy.SnapshotFields). The fields the
	// interval line wanted are gone from the contract, so there is nothing to fill and nothing to
	// leave zero: a contract with an unused field is a promise nobody keeps.
	in := assemble.Inputs{
		UnitID: req.UnitID, Generation: req.Generation,

		Copies:          copies,
		SaveInFlight:    inFlight != "",
		SaveStartedAt:   started,
		SaveProgress:    progress,
		CheckpointBytes: checkpointBytes,

		FreeBytes: free,

		Failures: req.Failures,

		Step:       step,
		StepSource: stepSource,

		Evidence: req.Evidence,
	}
	return assemble.Build(in), rep, nil
}

// abandonWindow is how quiet a write must be before it is called dead.
//
// Three writes or a minute, whichever is longer. Tying it to the measured write cost keeps the rule
// meaningful on slow storage, and the floor keeps a fresh deployment (no samples yet) from declaring
// every write dead after a second.
func abandonWindow(writeCosts []float64) time.Duration {
	window := 60 * time.Second
	if w, ok := p90(writeCosts); ok {
		if t := time.Duration(3 * w * float64(time.Second)); t > window {
			window = t
		}
	}
	return window
}

// quietFor is how long a directory has been still, on the caller's clock when it keeps one.
func quietFor(d stepDir, req Request) time.Duration {
	changed := d.changedAt
	if t, ok := req.Changed[d.name]; ok {
		changed = t
	}
	if changed.IsZero() {
		// Nothing to compare: the directory exists but no time is known for it. Treat it as just
		// seen, so an unknown age never turns into "abandoned" by accident.
		return 0
	}
	return req.Now.Sub(changed)
}

// inFlightOf picks the newest unsealed directory that was seen recently enough to be a write in
// progress, and returns its name together with when it was first seen.
func inFlightOf(unsealed []stepDir, seen map[string]time.Time, writeCosts []float64, now time.Time) (string, time.Time) {
	timeout := 60 * time.Second
	if w, ok := p90(writeCosts); ok {
		if t := time.Duration(3 * w * float64(time.Second)); t > timeout {
			timeout = t
		}
	}
	best, bestAt := "", time.Time{}
	for _, d := range unsealed {
		at := firstSeen(seen, d.name, d.changedAt)
		if at.IsZero() || now.Before(at) || now.Sub(at) >= timeout {
			continue
		}
		if at.After(bestAt) {
			best, bestAt = d.name, at
		}
	}
	return best, bestAt
}

// complete says "this copy is a usable checkpoint": sealed, or sealed and read back. A verified copy
// is not a different kind of checkpoint — forgetting that makes the controller believe it has none.
func complete(c assemble.Copy) bool {
	return c.State == assemble.Sealed || c.State == assemble.Verified
}

func newestSealed(copies []assemble.Copy) *assemble.Copy {
	var newest *assemble.Copy
	for i := range copies {
		if !complete(copies[i]) {
			continue
		}
		if newest == nil || copies[i].Step > newest.Step {
			newest = &copies[i]
		}
	}
	return newest
}

// stepOf prefers the job's own counter and falls back to the newest sealed copy, which is a floor:
// the unit has reached at least that step.
func stepOf(fromJob *int64, newest *assemble.Copy) (int64, string) {
	if fromJob != nil && *fromJob > 0 {
		return *fromJob, "job"
	}
	if newest != nil {
		return newest.Step, "checkpoint"
	}
	return 0, "checkpoint"
}

func dirName(step int64) string { return "step-" + strconv.FormatInt(step, 10) }

// firstSeen is the recorded sighting, or the directory's own modification time when nothing has been
// recorded yet. The first round has no ledger, and without a fallback every existing checkpoint
// would look like no checkpoint at all.
func firstSeen(seen map[string]time.Time, name string, fallback time.Time) time.Time {
	if t, ok := seen[name]; ok && !t.IsZero() {
		return t
	}
	return fallback
}

// seenAt looks up the first sighting of a file inside a directory, then of the directory itself.
func seenAt(seen map[string]time.Time, dir, file string) time.Time {
	if file != "" {
		if t, ok := seen[dir+"/"+file]; ok {
			return t
		}
	}
	return seen[dir]
}

// p90 is the 90th percentile of the samples, used as the write cost the schedule is planned for.
func p90(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	index := (len(ordered)*9+9)/10 - 1 // ceil(0.9 * n) - 1, in integer arithmetic
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index], true
}

// diskUsage reports what the file system says about the volume holding dir.
func diskUsage(dir string) (free, total int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize)
	return int64(st.Bavail) * bsize, int64(st.Blocks) * bsize, nil
}
