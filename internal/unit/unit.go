// Package unit decides what one write unit is, and how many units may write at once on one volume.
//
// A unit is the set of processes that seal one checkpoint directory together (decision 0001).
// The directory is the identity; a member is only a process. This package exists so that the
// algorithm never learns what a pod, a rank or a gang is: it sees one UnitID and one Generation.
package unit

import "sort"

// Member is one process that writes into a checkpoint directory.
type Member struct {
	// ID identifies the process. A pod UID is the natural choice: it changes when the pod is
	// replaced, which is exactly the event that starts a new generation.
	ID string
	// Name names the unit (namespace/job). Empty falls back to the directory, which is always
	// unique and always present.
	Name string
	// Dir is the checkpoint directory the process writes into.
	Dir string
	// Volume names the storage the directory lives on. Empty means Dir, which keeps units on
	// different directories from blocking each other when nobody told us the volume.
	Volume string
	// Cluster names the cluster this member's world file describes. Empty means the deployment has
	// one cluster, which is what a single-process installation is: the single cluster is the special
	// case, not a different code path.
	Cluster string
	// WorldDir is the root of the world files this member reads (`<worldDir>/<cluster>/...`).
	// Empty means `<Dir>/world`, the layout `WithWorld` resolves for a laptop install.
	WorldDir string
	// BootID is the process's own start id, when the job reports one. It is the only reliable
	// signal that the writing set changed without the pod set changing (a restart inside a pod).
	BootID string
}

// Unit is one write unit: a directory, the processes sealing it, and a generation.
type Unit struct {
	ID         string
	Dir        string
	Volume     string
	Cluster    string
	WorldDir   string
	Generation int64
	BootID     string
	Members    []string
}

// Registry turns a member set into units and keeps the generation across syncs.
//
// It is not safe for concurrent use; the controller calls it from one goroutine per reconcile.
type Registry struct {
	state map[string]*state
}

type state struct {
	volume  string
	members map[string]bool
	bootID  string
	gen     int64
}

func NewRegistry() *Registry {
	return &Registry{state: map[string]*state{}}
}

// Sync replaces the member set with the one given and returns the units, sorted by directory.
//
// The generation changes when the writing set changes: a member appears or disappears, or a boot id
// changes. It does not change when members are merely re-listed, because a generation bump
// invalidates the decision a job is about to read.
func (r *Registry) Sync(members []Member) []Unit {
	grouped := map[string][]Member{}
	for _, m := range members {
		if m.Dir == "" || m.ID == "" {
			continue
		}
		grouped[m.Dir] = append(grouped[m.Dir], m)
	}

	// A directory that disappeared keeps its state with no members, so that a unit that comes back
	// is a new generation rather than a silent continuation of a stale decision.
	for dir, st := range r.state {
		if _, ok := grouped[dir]; !ok {
			st.members = map[string]bool{}
		}
	}

	dirs := make([]string, 0, len(grouped))
	for dir := range grouped {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	out := make([]Unit, 0, len(dirs))
	for _, dir := range dirs {
		group := grouped[dir]
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })

		set := map[string]bool{}
		for _, m := range group {
			set[m.ID] = true
		}
		boot := bootOf(group)
		name := nameOf(group, dir)
		volume := volumeOf(group, dir)
		cluster := clusterOf(group)
		world := worldOf(group)

		st, ok := r.state[dir]
		if !ok {
			st = &state{members: map[string]bool{}}
			r.state[dir] = st
		}
		changed := !sameSet(st.members, set) ||
			(st.bootID != "" && boot != "" && st.bootID != boot)
		if changed || st.gen == 0 {
			st.gen++
		}
		st.volume, st.members = volume, set
		// An unknown boot id is not evidence of a change, and it must not erase the last id we
		// knew: the next sync would otherwise forget that the process restarted.
		if boot != "" {
			st.bootID = boot
		}

		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out = append(out, Unit{
			ID: name, Dir: dir, Volume: volume, Cluster: cluster, WorldDir: world,
			Generation: st.gen, BootID: boot, Members: ids,
		})
	}
	return out
}

// bootOf picks one boot id deterministically when several members report one: the smallest string.
// A rank that restarts changes its own id, and the unit's generation must follow.
func bootOf(group []Member) string {
	best := ""
	for _, m := range group {
		if m.BootID == "" {
			continue
		}
		if best == "" || m.BootID < best {
			best = m.BootID
		}
	}
	return best
}

func nameOf(group []Member, dir string) string {
	for _, m := range group {
		if m.Name != "" {
			return m.Name
		}
	}
	return dir
}

func volumeOf(group []Member, dir string) string {
	for _, m := range group {
		if m.Volume != "" {
			return m.Volume
		}
	}
	return dir
}

// clusterOf picks the cluster the unit's members agree on. Empty means the single cluster a
// single-process install has; two members naming different clusters is a deployment mistake, and
// the first one in id order wins so the result is deterministic rather than random.
func clusterOf(group []Member) string {
	for _, m := range group {
		if m.Cluster != "" {
			return m.Cluster
		}
	}
	return ""
}

func worldOf(group []Member) string {
	for _, m := range group {
		if m.WorldDir != "" {
			return m.WorldDir
		}
	}
	return ""
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// Level returns the generation of a directory, or 0 when it has never been seen.
func (r *Registry) Level(dir string) int64 {
	if st, ok := r.state[dir]; ok {
		return st.gen
	}
	return 0
}
