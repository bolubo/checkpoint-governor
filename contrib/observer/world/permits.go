package world

import "sort"

// assignPermits decides which units may spend a write when a higher-priority claim is waiting.
//
// This is the one question a governor cannot answer from where it stands (0021): its view is one
// unit, and "am I the one that gets taken" is a fact about the set. The observer is the only
// component that sees the whole node, so the verdict is computed here and published per unit.
//
// Only the reclaim case is inferred from the cluster today. A drain and shared damage are not
// detected here yet, and units therefore keep the permission they already had — which, absent a
// verdict, is permission.
func assignPermits(units []UnitRecord, need int) {
	if need <= 0 {
		return // nothing is waiting: there is no cause, so there is nothing to coordinate
	}

	byNode := map[string][]int{}
	for i := range units {
		if units[i].Node == "" {
			continue // not placed yet: it cannot be the victim of a reclaim that has not happened
		}
		byNode[units[i].Node] = append(byNode[units[i].Node], i)
	}

	for _, idx := range byNode {
		if len(idx) < 2 {
			continue // one unit on a node needs no coordination: it is the only candidate
		}
		ordered := append([]int(nil), idx...)
		// The scheduler takes the cheapest first: lowest priority, then the largest footprint, then
		// a stable name so two reads of the same world agree.
		sort.Slice(ordered, func(a, b int) bool {
			ua, ub := units[ordered[a]], units[ordered[b]]
			if ua.Priority != ub.Priority {
				return ua.Priority < ub.Priority
			}
			return ua.UnitID < ub.UnitID
		})

		victims := need
		if victims > len(ordered) {
			victims = len(ordered)
		}
		for rank, k := range ordered {
			verdict := "held"
			if rank < victims {
				verdict = "granted"
			}
			units[k].Permit = &Permit{Verdict: verdict, Cause: "reclaim", Rank: rank + 1}
		}
	}
}
