package permit

import (
	"testing"
	"time"
)

// Every failure of the channel is permission. A coordination layer that can suppress a save fails
// exactly when the fleet is under pressure, which is the worst moment to fail in this direction.
func TestAnythingUnreadableMeansGranted(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"empty":                            "",
		"unknown word":                     "maybe",
		"a spelling from a newer observer": "pending-v2",
		"capitals":                         "HELD",
		"whitespace":                       " held",
	}
	for name, raw := range cases {
		if got := Parse(raw, time.Time{}, now); got != Granted {
			t.Errorf("%s: %q parsed as %q, want granted", name, raw, got)
		}
	}
}

// A queue with no end, or one that has already ended, is not a reason to stay silent.
func TestAQueueMustHaveAnEnd(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	if got := Parse("queued", time.Time{}, now); got != Granted {
		t.Errorf("queued with no until parsed as %q, want granted", got)
	}
	if got := Parse("queued", now.Add(-time.Second), now); got != Granted {
		t.Errorf("queued whose until has passed parsed as %q, want granted", got)
	}
	if got := Parse("queued", now.Add(time.Minute), now); got != Queued {
		t.Errorf("queued with a live until parsed as %q, want queued", got)
	}
	if got := Parse("held", time.Time{}, now); got != Held {
		t.Errorf("held parsed as %q, want held", got)
	}
}

func units() []Unit {
	return []Unit{
		{ID: "a", Priority: 100, Size: 1, AtRisk: 3},
		{ID: "b", Priority: 50, Size: 1, AtRisk: 9},
		{ID: "c", Priority: 50, Size: 1, AtRisk: 1},
	}
}

// A reclaim takes the cheapest units that free enough room: lowest priority first, never the ones with
// the most to lose. That is the scheduler's rule, and copying it is what makes the verdict arguable.
//
// It does not split a tie. Two units at priority 50 are interchangeable to us — the Kueue experiment
// took the second of five identical holders — so holding one of them would suppress the write of the
// unit that dies with probability close to a half (0022).
func TestReclaimTakesTheCheapestAndNeverSplitsATie(t *testing.T) {
	got := Victims(units(), CauseReclaim, 1) // room for one unit, and two units tied at 50
	if len(got) != 2 {
		t.Fatalf("want both tied units, got %+v", got)
	}
	for _, u := range got {
		if u.Priority != 50 {
			t.Fatalf("the tie at 50 should be taken whole, got %+v", got)
		}
		if u.ID == "a" {
			t.Fatalf("the highest-priority unit was selected: %+v", got)
		}
	}

	// Two units of room, and exactly two units at the cheapest priority: no expansion beyond them.
	got = Victims(units(), CauseReclaim, 2)
	if len(got) != 2 {
		t.Fatalf("want two units, got %+v", got)
	}
	for _, u := range got {
		if u.ID == "a" {
			t.Fatalf("the highest-priority unit was selected: %+v", got)
		}
	}
}

// A drain takes everyone, and every one of them writes.
func TestDrainSelectsEveryone(t *testing.T) {
	got := Victims(units(), CauseDrain, 0)
	if len(got) != 3 {
		t.Fatalf("drain selected %d units, want all 3", len(got))
	}
}

// Shared damage orders by what is at risk, not by priority: priority decides who dies, not who loses
// most when the volume is the thing that is broken.
func TestSerializedOrdersByWhatIsAtRisk(t *testing.T) {
	got := Victims(units(), CauseSerialized, 0)
	if len(got) != 3 {
		t.Fatalf("serialized selected %d units, want all 3", len(got))
	}
	if got[0].ID != "b" || got[2].ID != "c" {
		t.Fatalf("want order b(9) a(3) c(1), got %+v", got)
	}
}

// An unknown cause selects nobody new, which leaves every unit with the permission it already had.
func TestAnUnknownCauseChangesNothing(t *testing.T) {
	if got := Victims(units(), Cause("meteor"), 0); got != nil {
		t.Fatalf("an unknown cause selected %+v", got)
	}
}

// This is the number the whole design exists for: ten units, one death, one write.
func TestTenUnitsAndOneDeathOneWrite(t *testing.T) {
	var fleet []Unit
	for i := 0; i < 10; i++ {
		fleet = append(fleet, Unit{ID: string(rune('a' + i)), Priority: 100 - i, Size: 1})
	}
	v := Verdicts(fleet, CauseReclaim, 1, false)

	granted := 0
	for _, verdict := range v {
		if verdict == Granted {
			granted++
		}
	}
	if granted != 1 {
		t.Fatalf("%d units may write for one death, want 1", granted)
	}
}
