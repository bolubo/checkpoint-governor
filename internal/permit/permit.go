// Package permit decides, for one cause, which units should spend a write.
//
// The algorithm answers "is my work at risk" for one unit and never sees the fleet (0010, 0021). The
// question "how many writes does this event justify" cannot be answered by any single unit, because a
// unit that sees only itself cannot tell "I am the victim" from "my neighbour is". This package answers
// it from the set, and it is the only place that is allowed to say no to a write.
//
// The bias is deliberate and one-directional: **every failure here is permission.** A channel that can
// suppress a save fails exactly when the fleet is under pressure, which is the worst possible moment.
package permit

import "time"

// Verdict is one unit's permission to write now.
type Verdict string

const (
	// Granted means the unit may write. It is also what every unreadable value means.
	Granted Verdict = "granted"
	// Held means this unit is not the one the cause selects; another unit is.
	Held Verdict = "held"
	// Queued means the unit may write later, in the order the cause gives.
	Queued Verdict = "queued"
)

// Parse reads a verdict from the world file. Anything it does not understand is Granted:
// a missing field, an empty string, a spelling from a newer observer, an expired `until`.
//
// `until` bounds a Queued verdict; zero means no bound, which is read as Granted rather than as an
// indefinite wait — a queue with no end is a save that never happens.
func Parse(raw string, until, now time.Time) Verdict {
	switch Verdict(raw) {
	case Held:
		// A hold has no expiry of its own: the observer re-publishes the world every round, and a
		// stale world is already refused by the conditions that read it.
		return Held
	case Queued:
		if until.IsZero() || !now.Before(until) {
			return Granted
		}
		return Queued
	default:
		return Granted
	}
}

// Cause is why several units look endangered at once. The fanout — how many writes this event
// justifies — is a property of the cause, not of how many units noticed (0021).
type Cause string

const (
	// CauseReclaim is a preemption: the scheduler takes a few units out of many.
	CauseReclaim Cause = "reclaim"
	// CauseDrain takes every unit on a node, so every unit writes.
	CauseDrain Cause = "drain"
	// CauseSerialized is shared damage (a full volume): every unit wants to write, and they must take
	// turns or make the damage worse.
	CauseSerialized Cause = "serialized"
)

// Unit is what the fleet knows about one candidate. Size is the room it occupies (in whatever unit the
// cause measures a shortfall in); AtRisk is how much progress would be lost if it died now.
type Unit struct {
	ID       string
	Priority int
	Size     int
	AtRisk   float64
}

// Victims returns the units that should write, in the order they should start.
//
// `need` is the shortfall the cause has to free; it is only meaningful for CauseReclaim, where a
// reclaim frees room until the incoming workload fits. Zero or negative means "one unit suffices".
func Victims(units []Unit, cause Cause, need int) []Unit {
	if len(units) == 0 {
		return nil
	}

	switch cause {
	case CauseDrain:
		// Everyone on the node dies. Every unit writes, in a stable order so two runs agree.
		out := append([]Unit(nil), units...)
		sortBy(out, func(a, b Unit) bool { return a.ID < b.ID })
		return out

	case CauseSerialized:
		// Everyone must write, but one at a time, and the one with the most to lose goes first:
		// priority decides who dies, not who loses most.
		out := append([]Unit(nil), units...)
		sortBy(out, func(a, b Unit) bool { return a.AtRisk > b.AtRisk })
		return out

	case CauseReclaim:
		// The scheduler takes the cheapest units that free enough room: lowest priority first, then
		// the largest footprint (it is the one that frees the most for the same death).
		ordered := append([]Unit(nil), units...)
		sortBy(ordered, func(a, b Unit) bool {
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			if a.Size != b.Size {
				return a.Size > b.Size
			}
			return a.ID < b.ID
		})
		want := need
		if want <= 0 {
			want = ordered[0].Size
		}
		var out []Unit
		freed := 0
		for _, u := range ordered {
			if freed >= want {
				break
			}
			out = append(out, u)
			freed += u.Size
		}
		// A tie is never broken. If the cut falls inside a group of equal priority, the choice between
		// those units is a coin flip — the Kueue experiment took the second of five identical holders —
		// and holding the one that dies suppresses the only write that mattered. So the whole tied
		// group writes, and the extra writes are the price of not guessing (0022).
		if len(out) > 0 {
			cut := out[len(out)-1].Priority
			for _, u := range ordered[len(out):] {
				if u.Priority != cut {
					break
				}
				out = append(out, u)
			}
		}
		return out
	}

	// An unknown cause selects nobody new: every unit keeps the permission it had. Refusing here would
	// be this package inventing a cause it does not understand.
	return nil
}

// Verdicts turns the selection into one verdict per unit, which is what the world file carries.
func Verdicts(units []Unit, cause Cause, need int, queued bool) map[string]Verdict {
	out := make(map[string]Verdict, len(units))
	chosen := map[string]bool{}
	for _, u := range Victims(units, cause, need) {
		chosen[u.ID] = true
	}
	for _, u := range units {
		switch {
		case !chosen[u.ID]:
			// Only a cause that takes *some* of the set may hold the rest. Drain and serialized
			// select everyone, so nothing lands here for them.
			out[u.ID] = Held
		case queued:
			out[u.ID] = Queued
		default:
			out[u.ID] = Granted
		}
	}
	return out
}

// sortBy is an insertion sort: the sets here are units on one node, and a stable order that reads in
// one screen is worth more than asymptotics.
func sortBy(units []Unit, less func(a, b Unit) bool) {
	for i := 1; i < len(units); i++ {
		for j := i; j > 0 && less(units[j], units[j-1]); j-- {
			units[j], units[j-1] = units[j-1], units[j]
		}
	}
}
