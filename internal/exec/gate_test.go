package exec

import (
	"fmt"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func save() policy.Decision {
	return policy.Decision{
		UnitID: "u", Generation: 1, Action: policy.ActionSave, Reason: policy.ReasonRisk,
		Reasons: []policy.ReasonEntry{{Code: "risk.preempt"}},
	}
}

func TestTheGateRefusesWhatIsNotASave(t *testing.T) {
	skip := policy.Decision{Action: policy.ActionSkip, Reason: policy.ReasonCalm}
	if got := Gate(skip, nil, Env{}, t0); got.Verdict != Refuse || got.Reason != "not-a-save" {
		t.Fatalf("gate(skip) = %+v", got)
	}
}

func TestTheGateHoldsWhileAWriteIsRunning(t *testing.T) {
	running := []Attempt{{ID: "a1", StartedAt: t0, Outcome: Running}}
	if got := Gate(save(), running, Env{Step: 400}, t0); got.Verdict != Hold {
		t.Fatalf("gate with a running attempt = %+v, want hold", got)
	}
}

// TestTheFourDoors is the truth table of E2. An attempt with no measured duration (cost 0) gives
// the documented floors: a 60s cooldown and a 120s first backoff.
func TestTheFourDoors(t *testing.T) {
	// The attempt is built the way the loop builds one, so its identity is the one the gate recomputes.
	done := NewAttempt("u", 1, 400, TriggerCondition, "risk.preempt", t0, t0.Add(time.Minute))
	done.EndedAt = t0
	done.Outcome = Sealed

	// cooldown: the last write ended a minute is not enough if the floor is 60s.
	got := Gate(save(), []Attempt{done}, Env{Step: 401, CheckpointBytes: 1000, FreeBytes: 10 * 1000}, t0)
	if got.Verdict != Refuse || got.Reason != "cooldown" {
		t.Fatalf("gate at the instant of a seal = %+v, want cooldown", got)
	}
	fmt.Printf("cooldown %ds\n", int(Cooldown(0).Seconds()))

	// The cooldown passes: a minute and a half later the gate starts.
	if got := Gate(save(), []Attempt{done}, Env{Step: 401, CheckpointBytes: 1000, FreeBytes: 10 * 1000}, t0.Add(90*time.Second)); got.Verdict != Start {
		t.Fatalf("gate 90s after a seal = %+v, want start", got)
	}

	// backoff: the same condition ended without sealing once, so the pause doubles.
	failed := done
	failed.Outcome = Abandoned
	got = Gate(save(), []Attempt{failed}, Env{Step: 401, CheckpointBytes: 1000, FreeBytes: 10 * 1000}, t0.Add(70*time.Second))
	if got.Verdict != Refuse || got.Reason != "backoff" {
		t.Fatalf("gate 70s after an abandoned attempt = %+v, want backoff", got)
	}
	fmt.Printf("backoff %ds\n", int(Backoff(0, 1).Seconds()))

	// headroom: the new copy and the one that stays have to coexist, so 1.2x free is not enough.
	tight := Env{Step: 401, CheckpointBytes: 1000, FreeBytes: 1200, Retained: 1}
	got = Gate(save(), []Attempt{done}, tight, t0.Add(90*time.Second))
	if got.Verdict != Refuse || got.Reason != "headroom" {
		t.Fatalf("gate with 1.2x headroom = %+v, want headroom", got)
	}
	fmt.Println(FormatHeadroom(tight))

	// headroom is a space rule, not the constant 2x: two retained copies need three checkpoints,
	// and a cold start has nothing to coexist with, so one is enough.
	two := Env{Step: 401, CheckpointBytes: 1000, FreeBytes: 2500, Retained: 2}
	if got := Gate(save(), []Attempt{done}, two, t0.Add(90*time.Second)); got.Verdict != Refuse || got.Reason != "headroom" {
		t.Fatalf("gate with 2.5x free and 2 retained = %+v, want headroom", got)
	}
	fmt.Println(FormatHeadroom(two))
	cold := Env{Step: 401, CheckpointBytes: 1000, FreeBytes: 1500}
	if got := Gate(save(), []Attempt{done}, cold, t0.Add(90*time.Second)); got.Verdict != Start {
		t.Fatalf("gate with 1.5x free and nothing retained = %+v, want start: cold start needs one", got)
	}

	// An unknown is not a zero: with no size there is no scale, and the write starts with the caveat
	// said out loud instead of passing silently.
	unknown := Env{Step: 401, FreeBytes: 10 * 1000}
	got = Gate(save(), nil, unknown, t0)
	if got.Verdict != Start || got.Warning == "" {
		t.Fatalf("gate with an unknown size = %+v, want start plus a warning", got)
	}
	fmt.Printf("gate: %s\n", FormatHeadroom(unknown))
	noFree := Env{Step: 401, CheckpointBytes: 1000, FreeBytes: -1}
	if got := Gate(save(), nil, noFree, t0); got.Verdict != Start || got.Warning == "" {
		t.Fatalf("gate with unknown free space = %+v, want start plus a warning", got)
	}

	// duplicate: the very same request, whatever the clock says.
	got = Gate(save(), []Attempt{done}, Env{Step: 400, CheckpointBytes: 1000, FreeBytes: 10 * 1000}, t0.Add(time.Hour))
	if got.Verdict != Refuse || got.Reason != "duplicate" {
		t.Fatalf("gate on a repeated request = %+v, want duplicate", got)
	}
}

// A held reclaim is a new event with a deadline we do not own: the cooldown must not make it wait,
// or the gate's own deadline passes while the victim counts down a pause meant for a repeat. Backoff
// still applies, because a write that keeps failing is a fact, not a throttle (0030).
func TestAHeldReclaimIsExemptFromCooldownButNotBackoff(t *testing.T) {
	done := NewAttempt("u", 1, 400, TriggerCondition, "risk.preempt", t0, t0.Add(time.Minute))
	done.EndedAt = t0
	done.Outcome = Sealed
	env := Env{Step: 401, CheckpointBytes: 1000, FreeBytes: 10 * 1000}

	env.Reclaim = true
	if got := Gate(save(), []Attempt{done}, env, t0.Add(5*time.Second)); got.Verdict != Start {
		t.Fatalf("a reclaim five seconds after a seal = %+v, want start", got)
	}
	env.Reclaim = false
	if got := Gate(save(), []Attempt{done}, env, t0.Add(5*time.Second)); got.Verdict != Refuse || got.Reason != "cooldown" {
		t.Fatalf("the same write without a reclaim = %+v, want cooldown", got)
	}

	failed := done
	failed.Outcome = Abandoned
	env.Reclaim = true
	if got := Gate(save(), []Attempt{failed}, env, t0.Add(5*time.Second)); got.Verdict != Refuse || got.Reason != "backoff" {
		t.Fatalf("a reclaim after a failed write = %+v, want backoff", got)
	}
}

// TestARefusalIsNeitherACooldownNorAFailure: the gate said no, so nothing was written. Counting that
// as a finished write would reset the cooldown clock ("we said no a second ago") and grow the
// backoff ("we said no three times"), which is how a gate ends up refusing forever.
func TestARefusalIsNeitherACooldownNorAFailure(t *testing.T) {
	refused := NewAttempt("u", 1, 400, TriggerCondition, "risk.preempt", t0.Add(-time.Second), t0)
	refused.Outcome, refused.EndedAt = Refused, t0

	// A different step, so the duplicate door does not answer first; the clock is the instant of the
	// refusal, which is exactly when a cooldown would have bitten.
	if got := Gate(save(), []Attempt{refused}, Env{Step: 401}, t0); got.Verdict != Start {
		t.Fatalf("gate right after a refusal = %+v, want start: a refusal is not a write", got)
	}
	// The very same request is allowed again: it was never served, so it must not be deduplicated
	// into silence — that is how a condition that keeps firing would never write at all.
	if got := Gate(save(), []Attempt{refused}, Env{Step: 400}, t0); got.Verdict != Start {
		t.Fatalf("gate on a repeated refused request = %+v, want start", got)
	}
	// The same reason and a different step: no backoff, because nothing failed.
	failed := refused
	failed.Outcome = Failed
	if got := Gate(save(), []Attempt{failed}, Env{Step: 401}, t0); got.Verdict == Start {
		t.Fatalf("gate right after a failure = %+v, want a pause", got)
	}
}

func TestTheBackoffDoublesAndCaps(t *testing.T) {
	base := Cooldown(30) // 90s for a 30s write
	for n, want := range map[int]time.Duration{0: base, 1: 2 * base, 2: 4 * base} {
		if got := Backoff(30, n); got != want {
			t.Fatalf("backoff(30, %d) = %s, want %s", n, got, want)
		}
	}
	if got := Backoff(30, 10); got != MaxBackoff {
		t.Fatalf("backoff cap = %s, want %s", got, MaxBackoff)
	}
}
