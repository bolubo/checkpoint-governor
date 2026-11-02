package advise

import "sort"

// firstSeenWindow is how many paths the ledger remembers the first sighting of.
//
// The number only backs one measurement — "how long was this write open", computed as seal-seen minus
// first-seen — so it only has to cover writes that could still be open. Everything else was a leak: one entry
// per directory ever written, for the life of the job, in a map that is read and rewritten every round.
const firstSeenWindow = 256

// pruneFirstSeen keeps the newest sightings and drops the rest.
//
// Newest, not oldest, because the recent ones are the ones a seal can still be matched against. Dropping an
// entry costs one measurement of a write that finished long ago, and this map is not a record of history —
// the trace is.
func (l *Ledger) pruneFirstSeen() {
	if len(l.FirstSeen) <= firstSeenWindow {
		return
	}
	type seen struct {
		path string
		at   int64
	}
	all := make([]seen, 0, len(l.FirstSeen))
	for path, at := range l.FirstSeen {
		all = append(all, seen{path: path, at: at})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at > all[j].at })

	kept := make(map[string]int64, firstSeenWindow)
	for _, e := range all[:firstSeenWindow] {
		kept[e.path] = e.at
	}
	l.FirstSeen = kept
}
