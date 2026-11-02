package exec

import (
	"testing"
	"time"
)

// A preemption is an event, not a rate. The first write for it is the answer; while the same event is
// in force, nothing new is written — the window keeps the checkpoint fresh from there (0017, 0022).
func TestOneWritePerRiskEvent(t *testing.T) {
	d, env, now := benign()
	env.CauseID = "reclaim/pending-7f3a"

	if r := Gate(d, nil, env, now); r.Verdict != Start {
		t.Fatalf("the first write for a new event was refused: %q/%q", r.Verdict, r.Reason)
	}

	done := env
	done.CauseHandled = true
	for i := 1; i <= 9; i++ {
		round := now.Add(time.Duration(i) * 5 * time.Second)
		r := Gate(d, nil, done, round)
		if r.Verdict != Refuse || r.Reason != "already-written-for-this-risk" {
			t.Fatalf("round %d wrote again for the same event: %q/%q", i, r.Verdict, r.Reason)
		}
		if r.Detail == "" {
			t.Fatalf("round %d refused without naming the event", i)
		}
	}
}

// A different arrival is a different event: its own decision, and its own write.
func TestANewRiskEventWritesAgain(t *testing.T) {
	d, env, now := benign()
	env.CauseID = "reclaim/pending-0001" // the earlier event is over
	env.CauseHandled = false

	if r := Gate(d, nil, env, now.Add(time.Hour)); r.Verdict != Start {
		t.Fatalf("a new event was treated as the old one: %q/%q", r.Verdict, r.Reason)
	}
}

// With no id there is nothing to compare. Refusing on that would suppress exactly the write the risk
// exists to justify, so an unreadable cause always writes.
func TestAnUnreadableCauseAlwaysWrites(t *testing.T) {
	d, env, now := benign()
	env.CauseHandled = true // the ledger says handled, but with no id it cannot be this event
	env.CauseID = ""

	if r := Gate(d, nil, env, now); r.Verdict != Start {
		t.Fatalf("an unreadable cause suppressed a write: %q/%q", r.Verdict, r.Reason)
	}
}
