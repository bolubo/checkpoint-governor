package policy

import "testing"

// A condition added from disk is watched exactly like a built-in one: urgent fires a save, and the
// decision names it.
func TestAConditionFromDiskCanFire(t *testing.T) {
	custom := Condition{Name: "job.loss-exploded", Tier: TierUrgent, Missing: MissingUrgent,
		Why: "the loss left its normal range"}

	d := Risk{Extra: []Condition{custom}}.Decide(
		Bounds{NoUs: NoUsNone, MinCopies: 1},
		Snapshot{UnitID: "u", Evidence: map[string]Observation{
			"job.loss-exploded": {Kind: Boolean, Bool: true},
		}})

	if d.Action != ActionSave || d.Reason != ReasonRisk || !d.SaveNow {
		t.Fatalf("decision = %s/%s saveNow=%v, want save/risk", d.Action, d.Reason, d.SaveNow)
	}
	if d.Why != "job.loss-exploded: the loss left its normal range" {
		t.Fatalf("why = %q, want the added condition named", d.Why)
	}
}

// Precedence is consequence: a built-in urgent condition outranks an added one, so the decision names
// the fact this repository owns rather than the one an operator typed.
func TestABuiltInConditionOutranksAnAddedOne(t *testing.T) {
	custom := Condition{Name: "job.loss-exploded", Tier: TierUrgent, Missing: MissingUrgent, Why: "loss"}

	d := Risk{Extra: []Condition{custom}}.Decide(
		Bounds{NoUs: NoUsNone, MinCopies: 1},
		Snapshot{UnitID: "u", Evidence: map[string]Observation{
			"risk.shutdown":     {Kind: Boolean, Bool: true},
			"job.loss-exploded": {Kind: Boolean, Bool: true},
		}})

	if d.Reasons[0].Code != "risk.shutdown" {
		t.Fatalf("the decision names %q first, want risk.shutdown", d.Reasons[0].Code)
	}
}

// An added condition that cannot be read follows its own missing policy, like a built-in one would.
func TestAnAddedConditionThatCannotBeReadIsBlind(t *testing.T) {
	custom := Condition{Name: "job.loss-exploded", Tier: TierUrgent, Missing: MissingUrgent, Why: "loss"}

	d := Risk{Extra: []Condition{custom}}.Decide(
		Bounds{NoUs: NoUsNone, MinCopies: 1},
		Snapshot{UnitID: "u", Evidence: map[string]Observation{
			"job.loss-exploded": {Kind: Unknown},
		}})

	if d.Action != ActionSave {
		t.Fatalf("action = %s, want save: an unreadable urgent condition is not calm", d.Action)
	}
	if len(d.Missing) == 0 || d.Missing[0] != "blind:job.loss-exploded" {
		t.Fatalf("missing = %v, want blind:job.loss-exploded", d.Missing)
	}
}

// The zero value is the frozen catalogue: every caller that never loads a directory keeps the
// contract pinned by condition_test.go.
func TestTheZeroValueWatchesTheBuiltInCatalogue(t *testing.T) {
	if got := len(Risk{}.catalogue()); got != len(conditionCatalog) {
		t.Fatalf("zero value watches %d conditions, want %d", got, len(conditionCatalog))
	}
}
