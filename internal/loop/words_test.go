package loop

import (
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// A firing condition and an unreadable one both begin their sentence with the condition's name. The
// log line is what a person reads while the job runs, so it must not rename a real save "cannot read":
// this regression printed "save risk.preempt cannot read: …" for every firing condition.
func TestWordsForTellsAFiringConditionFromABlindOne(t *testing.T) {
	fired := policy.Decision{
		Action:  policy.ActionSave,
		Reason:  policy.ReasonRisk,
		Why:     "risk.preempt: higher-priority or more urgent work can take these resources",
		Reasons: []policy.ReasonEntry{{Code: "risk.preempt"}},
	}
	cause, why := wordsFor(fired, policy.ActionSave)
	if cause != "risk.preempt" {
		t.Fatalf("cause = %q, want risk.preempt", cause)
	}
	if strings.HasPrefix(why, "cannot read") {
		t.Fatalf("a firing condition was reported as blind: %q", why)
	}
	if want := "higher-priority or more urgent work can take these resources"; why != want {
		t.Fatalf("why = %q, want %q", why, want)
	}

	blind := policy.Decision{
		Action:  policy.ActionSave,
		Reason:  policy.ReasonRisk,
		Why:     "cannot read risk.preempt: the observer tracks units but has not placed this one",
		Reasons: []policy.ReasonEntry{{Code: "risk.preempt"}},
	}
	cause, why = wordsFor(blind, policy.ActionSave)
	if cause != "risk.preempt" {
		t.Fatalf("blind cause = %q, want risk.preempt", cause)
	}
	if want := "cannot read: the observer tracks units but has not placed this one"; why != want {
		t.Fatalf("blind why = %q, want %q", why, want)
	}
}
