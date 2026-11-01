package world

import "testing"

func fleet() []UnitRecord {
	return []UnitRecord{
		{UnitID: "u1", Node: "n1", Priority: 100},
		{UnitID: "u2", Node: "n1", Priority: 100},
		{UnitID: "u3", Node: "n1", Priority: 100},
		{UnitID: "u4", Node: "n1", Priority: 100},
		{UnitID: "u5", Node: "n1", Priority: 50},
	}
}

// Nothing waiting means no cause, and no permission is written: the governors keep behaving as they
// always did, which is the property that makes this field safe to add.
func TestNothingWaitingWritesNoVerdicts(t *testing.T) {
	units := fleet()
	assignPermits(units, 0)
	for _, u := range units {
		if u.Permit != nil {
			t.Fatalf("%s got a verdict with nothing waiting: %+v", u.UnitID, u.Permit)
		}
	}
}

// One claim, one victim: the cheapest unit on the node is the one that may write, and the rest are
// held. This is the difference between one write and five for a single death.
func TestOneClaimHoldsEveryUnitButTheCheapest(t *testing.T) {
	units := fleet()
	assignPermits(units, 1)

	granted := []string{}
	for _, u := range units {
		if u.Permit == nil {
			t.Fatalf("%s got no verdict while a claim was waiting", u.UnitID)
		}
		if u.Permit.Verdict == "granted" {
			granted = append(granted, u.UnitID)
		}
		if u.Permit.Cause != "reclaim" {
			t.Errorf("%s: cause %q, want reclaim", u.UnitID, u.Permit.Cause)
		}
	}
	if len(granted) != 1 || granted[0] != "u5" {
		t.Fatalf("granted %v, want exactly the lowest-priority unit u5", granted)
	}
	for _, u := range units {
		if u.UnitID == "u5" && u.Permit.Rank != 1 {
			t.Errorf("the selected unit has rank %d, want 1", u.Permit.Rank)
		}
		if u.UnitID == "u1" && u.Permit.Verdict != "held" {
			t.Errorf("a higher-priority unit was not held: %+v", u.Permit)
		}
	}
}

// Two claims take two units, cheapest first.
func TestTwoClaimsTakeTwo(t *testing.T) {
	units := fleet()
	assignPermits(units, 2)
	granted := 0
	for _, u := range units {
		if u.Permit.Verdict == "granted" {
			granted++
		}
	}
	if granted != 2 {
		t.Fatalf("%d units may write for two claims, want 2", granted)
	}
}

// A node with one unit needs no coordination, and is left exactly as it was: this is what keeps a
// single-job deployment untouched.
func TestALonelyUnitGetsNoVerdict(t *testing.T) {
	units := []UnitRecord{{UnitID: "solo", Node: "n1", Priority: 10}}
	assignPermits(units, 1)
	if units[0].Permit != nil {
		t.Fatalf("a single unit on a node got %+v, want nothing", units[0].Permit)
	}
}

// Units on different nodes never compete; a claim on one node does not hold a unit on another.
func TestNodesDoNotCompete(t *testing.T) {
	units := []UnitRecord{
		{UnitID: "a1", Node: "n1", Priority: 10},
		{UnitID: "a2", Node: "n1", Priority: 20},
		{UnitID: "b1", Node: "n2", Priority: 10},
		{UnitID: "b2", Node: "n2", Priority: 20},
	}
	assignPermits(units, 1)
	for _, u := range units {
		if u.Permit == nil {
			t.Fatalf("%s got no verdict", u.UnitID)
		}
	}
	// Each node grants its own cheapest unit: a1 and b1.
	if units[0].Permit.Verdict != "granted" || units[2].Permit.Verdict != "granted" {
		t.Fatalf("each node should grant its cheapest: %+v %+v", units[0].Permit, units[2].Permit)
	}
}
