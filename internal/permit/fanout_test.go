package permit

import "testing"

// The measured case: five identical holders, one arrival, one death. A pick among them is right about
// a fifth of the time, and one write is 1.2s against hours of training, so the group writes.
func TestIndistinguishableUnitsAllWrite(t *testing.T) {
	got := Decide(5, 1, 1.2, 21600, 1.0/5)
	if got.Writes != 5 || got.Reason != "indistinguishable" {
		t.Fatalf("got %d writes (%s), want the whole group", got.Writes, got.Reason)
	}
	if got.Tax >= got.Loss {
		t.Fatalf("tax %.1f should be smaller than the expected loss %.1f", got.Tax, got.Loss)
	}
}

// When the scheduler's ordering separates the candidates, the pick is reliable and only the victims
// pay for a write.
func TestDistinguishableUnitsWriteOnlyTheVictims(t *testing.T) {
	got := Decide(5, 1, 1.2, 21600, 1)
	if got.Writes != 1 || got.Reason != "only-the-victims" {
		t.Fatalf("got %d writes (%s), want 1", got.Writes, got.Reason)
	}
}

// c5: the same waiting higher-priority pod, and nobody dies. Nothing may be written.
func TestNobodyAtRiskWritesNothing(t *testing.T) {
	if got := Decide(5, 0, 1.2, 21600, 0.2); got.Writes != 0 {
		t.Fatalf("%d writes when nobody is expected to die", got.Writes)
	}
}

// A write that costs nothing to repeat makes coordination pointless: there is no tax to avoid.
func TestAFreeWriteNeverJustifiesHoldingAnyone(t *testing.T) {
	if got := Decide(10, 1, 0, 21600, 1); got.Writes != 10 {
		t.Fatalf("got %d writes with a free write, want everyone", got.Writes)
	}
}

// An unmeasurable cost must not be read as zero and silence the writes; the safe answer is the group.
func TestAnUnknownCostWritesTheGroup(t *testing.T) {
	if got := Decide(10, 1, 0, 0, 1); got.Writes != 10 || got.Reason != "cost-unknown" {
		t.Fatalf("got %d writes (%s), want the group marked cost-unknown", got.Writes, got.Reason)
	}
}

// A risk that takes everyone needs no arithmetic.
func TestEveryoneDies(t *testing.T) {
	if got := Decide(4, 4, 1.2, 21600, 0.25); got.Writes != 4 || got.Reason != "everyone-will-die" {
		t.Fatalf("got %d writes (%s)", got.Writes, got.Reason)
	}
}
