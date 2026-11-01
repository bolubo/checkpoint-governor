package exec

import (
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// benign is a request that every other door lets through, so a failure here is about the fleet door.
func benign() (policy.Decision, Env, time.Time) {
	d := policy.Decision{Action: policy.ActionSave, CopiesToWrite: 1}
	env := Env{Step: 5, CheckpointBytes: 1 << 20, FreeBytes: 1 << 40, Copies: 1}
	return d, env, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
}

// Without a permit field the gate behaves exactly as before: a single-unit deployment has no fleet.
func TestNoPermitIsTheOldBehaviour(t *testing.T) {
	d, env, now := benign()
	if r := Gate(d, nil, env, now); r.Verdict != Start {
		t.Fatalf("with no verdict the gate said %q (%s), want start", r.Verdict, r.Reason)
	}
}

// The one verdict that stops a write, and the sentence that explains it.
func TestHeldRefusesAndSaysWhoWon(t *testing.T) {
	d, env, now := benign()
	env.Permit = permit.Held
	r := Gate(d, nil, env, now)
	if r.Verdict != Refuse || r.Reason != "not-the-target" {
		t.Fatalf("held gave %q/%q, want refuse/not-the-target", r.Verdict, r.Reason)
	}
	if r.Detail == "" {
		t.Fatal("a refusal without a sentence cannot be acted on")
	}
}

// A queue is a wait, not a refusal: the unit will write when its turn comes.
func TestQueuedWaits(t *testing.T) {
	d, env, now := benign()
	env.Permit = permit.Queued
	env.PermitUntil = now.Add(3 * time.Second)
	r := Gate(d, nil, env, now)
	if r.Verdict != Hold || r.Wait != 3*time.Second {
		t.Fatalf("queued gave %q waiting %v, want hold/3s", r.Verdict, r.Wait)
	}
}

// A queue whose end has passed is a queue that is over.
func TestAnExpiredQueueStarts(t *testing.T) {
	d, env, now := benign()
	env.Permit = permit.Queued
	env.PermitUntil = now.Add(-time.Second)
	if r := Gate(d, nil, env, now); r.Verdict != Start {
		t.Fatalf("an expired queue gave %q, want start", r.Verdict)
	}
}

// The measurement this whole layer exists for, at the level the gate can prove it.
//
// Five units see the same announcement. Without a fleet verdict all five start a write for one death.
// With it, exactly one does — and the one that does is a unit the reclaim actually selects, so the
// recall of victims is unchanged. Reporting only the first number would be trading safety for a metric.
func TestFiveUnitsOneDeathOneWriteAndTheVictimStillWrites(t *testing.T) {
	fleet := []permit.Unit{
		{ID: "u1", Priority: 100, Size: 1},
		{ID: "u2", Priority: 100, Size: 1},
		{ID: "u3", Priority: 100, Size: 1},
		{ID: "u4", Priority: 100, Size: 1},
		{ID: "u5", Priority: 50, Size: 1}, // the one a reclaim takes first
	}
	d, env, now := benign()

	writesWithout := 0
	for range fleet {
		if Gate(d, nil, env, now).Verdict == Start {
			writesWithout++
		}
	}

	verdicts := permit.Verdicts(fleet, permit.CauseReclaim, 1, false)
	writesWith, victimWrote := 0, false
	for _, u := range fleet {
		e := env
		e.Permit = verdicts[u.ID]
		if Gate(d, nil, e, now).Verdict == Start {
			writesWith++
			if u.ID == "u5" {
				victimWrote = true
			}
		}
	}

	if writesWithout != 5 {
		t.Fatalf("the baseline should be one write per unit, got %d", writesWithout)
	}
	if writesWith != 1 {
		t.Fatalf("with the verdict: %d writes for one death, want 1 (writes per preemption 5:1 -> 1:1)", writesWith)
	}
	if !victimWrote {
		t.Fatal("the unit the reclaim actually takes did not write: recall fell, which no write count may buy")
	}
}
