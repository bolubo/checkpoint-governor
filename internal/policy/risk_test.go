package policy

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// calmSnapshot is a job with nothing wrong: no failures, no write in flight, no broken copy, and no
// conditions reported. It is the control case for every truth table below.
func calmSnapshot() Snapshot {
	return Snapshot{
		UnitID:     "train-1",
		Generation: 1,
		Step:       120,
		Copies:     []Copy{{Step: 100, Bytes: 300, State: CopyVerified}},
		Evidence:   map[string]Observation{},
	}
}

func calmBounds() Bounds {
	return Bounds{NoUs: NoUsNone, MinCopies: 1}
}

func report(s Snapshot, name string, value Observation) Snapshot {
	if s.Evidence == nil {
		s.Evidence = map[string]Observation{}
	}
	o := value
	if o.At.IsZero() {
		o.At = time.Unix(0, 0).UTC()
		o.Source = "test"
	}
	s.Evidence[name] = o
	return s
}

func boolean(v bool) Observation { return Observation{Kind: Boolean, Bool: v} }
func unknown() Observation       { return Observation{Kind: Unknown} }

// TestEachConditionTruthTable is the truth table the whole design rests on: for every condition,
// absent, 0, 1 and unreadable, and what each of them may and may not do.
func TestEachConditionTruthTable(t *testing.T) {
	cases := 0
	for _, c := range Conditions() {
		b := calmBounds()

		absent := (Risk{}).Decide(b, calmSnapshot())
		if absent.Action == ActionSave {
			t.Fatalf("%s: an absent condition caused a write", c.Name)
		}
		cases++

		zero := (Risk{}).Decide(b, report(calmSnapshot(), c.Name, boolean(false)))
		if zero.Action == ActionSave {
			t.Fatalf("%s: a 0 caused a write", c.Name)
		}
		cases++

		one := (Risk{}).Decide(b, report(calmSnapshot(), c.Name, boolean(true)))
		wantSave := c.Tier == TierUrgent
		if got := one.Action == ActionSave; got != wantSave {
			t.Fatalf("%s: 1 gave %q, wantSave=%v", c.Name, one.Action, wantSave)
		}
		if wantSave {
			if one.Reason != ReasonRisk || len(one.Reasons) == 0 || one.Reasons[0].Code != c.Name {
				t.Fatalf("%s: 1 gave reason %q / %v, want the condition named", c.Name, one.Reason, one.Reasons)
			}
			if got, want := one.Reasons[0].Evidence, []string{"Evidence[" + c.Name + "]"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: evidence = %v, want %v", c.Name, got, want)
			}
			if one.Why == "" {
				t.Fatalf("%s: 1 gave no sentence", c.Name)
			}
		} else if len(one.Reasons) == 0 || one.Reasons[0].Code != c.Name {
			t.Fatalf("%s: a shown condition should still be named in the reasons", c.Name)
		}
		cases++

		blind := (Risk{}).Decide(b, report(calmSnapshot(), c.Name, unknown()))
		wantBlindSave := c.Missing == MissingUrgent && c.Tier == TierUrgent
		if got := blind.Action == ActionSave; got != wantBlindSave {
			t.Fatalf("%s: unreadable gave %q, wantSave=%v", c.Name, blind.Action, wantBlindSave)
		}
		if wantBlindSave {
			found := false
			for _, m := range blind.Missing {
				if m == "blind:"+c.Name {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: unreadable did not name the blindness in Missing: %v", c.Name, blind.Missing)
			}
		} else if len(blind.Missing) != 0 {
			t.Fatalf("%s: unreadable but inert should not appear in Missing: %v", c.Name, blind.Missing)
		}
		cases++
	}
	fmt.Printf("truth table: %d cases over %d conditions\n", cases, len(Conditions()))
}

// TestDiagnosticsNeverTrigger: the six show-only conditions may be loud, but they must never ask for
// a write. Slowness is not lost work.
func TestDiagnosticsNeverTrigger(t *testing.T) {
	for _, c := range Conditions() {
		if c.Tier == TierUrgent {
			continue
		}
		all := calmSnapshot()
		for _, d := range Conditions() {
			if d.Tier != TierUrgent {
				all = report(all, d.Name, boolean(true))
			}
		}
		d := (Risk{}).Decide(calmBounds(), all)
		if d.Action != ActionSkip || d.Reason != ReasonCalm {
			t.Fatalf("all diagnostics on gave %q/%q, want a skip", d.Action, d.Reason)
		}
		if len(d.Reasons) == 0 {
			t.Fatalf("a quiet decision should still list what it is watching")
		}
		return
	}
}

// TestTheAlgorithmIsEnvironmentBlind: a CPU-shaped snapshot has no GPU condition at all; a GPU-shaped
// one has it and it reads false. The decision must be identical, because the algorithm does not know
// which environment it is in. Likewise, all conditions absent equals all conditions false.
func TestTheAlgorithmIsEnvironmentBlind(t *testing.T) {
	cpu := calmSnapshot()                                           // host.gpu-fault is absent, nothing else set
	gpu := report(calmSnapshot(), "host.gpu-fault", boolean(false)) // present and healthy
	if a, b := (Risk{}).Decide(calmBounds(), cpu), (Risk{}).Decide(calmBounds(), gpu); !reflect.DeepEqual(a, b) {
		t.Fatalf("CPU-shaped and GPU-shaped jobs decided differently:\n%+v\n%+v", a, b)
	}

	absent := calmSnapshot()
	allZero := calmSnapshot()
	for _, c := range Conditions() {
		allZero = report(allZero, c.Name, boolean(false))
	}
	if a, b := (Risk{}).Decide(calmBounds(), absent), (Risk{}).Decide(calmBounds(), allZero); !reflect.DeepEqual(a, b) {
		t.Fatalf("absent conditions and zero conditions decided differently:\n%+v\n%+v", a, b)
	}
}

// TestSafetyFactsBeatConditions: a write in flight, two failed writes, and a broken copy are bigger
// than any condition, and they must not be overridden by one that fires.
func TestSafetyFactsBeatConditions(t *testing.T) {
	firing := report(calmSnapshot(), "risk.preempt", boolean(true))

	inFlight := firing
	inFlight.SaveInFlight = true
	if d := (Risk{}).Decide(calmBounds(), inFlight); d.Action != ActionHold {
		t.Fatalf("a write in flight with a firing condition gave %q, want hold", d.Action)
	}

	failing := firing
	failing.ConsecutiveWriteFailures = 2
	if d := (Risk{}).Decide(calmBounds(), failing); d.Action != ActionInfeasible || d.Reason != ReasonRepair {
		t.Fatalf("two failures with a firing condition gave %q/%q, want a repair refusal", d.Action, d.Reason)
	}

	broken := firing
	broken.Copies = []Copy{{Step: 100, Bytes: 300, State: CopyBroken}}
	if d := (Risk{}).Decide(calmBounds(), broken); d.Action != ActionRepair || !d.SaveNow {
		t.Fatalf("a broken copy with a firing condition gave %q, want a repair", d.Action)
	}
}

// TestNoUsHasThreeStates: unset is the only state an executor must refuse to run with; none is the
// user's signature and must behave like "no writes"; every hands the job its own timer. And the
// window travels with the plan whatever the answer, because the job needs both numbers.
func TestNoUsHasThreeStates(t *testing.T) {
	unset := (Risk{}).Decide(Bounds{}, calmSnapshot())
	if unset.Action != ActionUnknown || unset.Reason != ReasonUnknown {
		t.Fatalf("an unanswered no-us gave %q/%q, want a refusal", unset.Action, unset.Reason)
	}
	found := false
	for _, m := range unset.Missing {
		if m == "no-us" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refusal does not name no-us: %v", unset.Missing)
	}

	none := (Risk{}).Decide(calmBounds(), calmSnapshot())
	if none.Action != ActionSkip || none.BaselineSeconds != 0 || !none.BaselineNone {
		t.Fatalf("no-us none gave %q with baseline %d/%v, want a skip and the none flag", none.Action, none.BaselineSeconds, none.BaselineNone)
	}

	every := (Risk{}).Decide(Bounds{NoUs: NoUsEvery, NoUsEvery: 30 * time.Minute, MinCopies: 1}, calmSnapshot())
	if every.Action != ActionSkip || every.BaselineSeconds != 1800 || every.BaselineNone {
		t.Fatalf("no-us every gave %q with baseline %d/%v, want the job's 30m", every.Action, every.BaselineSeconds, every.BaselineNone)
	}

	windowed := (Risk{}).Decide(Bounds{NoUs: NoUsNone, WithUs: time.Hour, MinCopies: 1}, calmSnapshot())
	if windowed.WindowSeconds != 3600 {
		t.Fatalf("with-us 1h travelled as %d seconds, want 3600", windowed.WindowSeconds)
	}
}

// TestABrokenProbeOutranksEverything: the catalogue's first entry is the probe being dead, because
// every other condition is something the probe reports. If the probe is gone, that is the truth the
// decision has to name, not whatever else happens to be true in that same snapshot.
func TestABrokenProbeOutranksEverything(t *testing.T) {
	s := report(calmSnapshot(), "risk.preempt", boolean(true))
	s = report(s, "watch.broken", boolean(true))
	d := (Risk{}).Decide(calmBounds(), s)
	if d.Action != ActionSave || d.Reasons[0].Code != "watch.broken" {
		t.Fatalf("got %q / %v, want watch.broken named first", d.Action, d.Reasons)
	}
}

// TestTheCanonicalFactsNeedNoCondition: a broken copy and repeated write failures are typed fields,
// not conditions, and one fact has one representation. They must work with no condition reported at
// all, so a deployment without producers still repairs what it can see about its own checkpoints.
func TestTheCanonicalFactsNeedNoCondition(t *testing.T) {
	broken := calmSnapshot()
	broken.Copies = []Copy{{Step: 100, Bytes: 300, State: CopyBroken}}
	d := (Risk{}).Decide(calmBounds(), broken)
	if d.Action != ActionRepair || d.Reasons[0].Code != codeRepairBrokenCopy {
		t.Fatalf("broken copy without conditions gave %q / %v", d.Action, d.Reasons)
	}

	failing := calmSnapshot()
	failing.ConsecutiveWriteFailures = 2
	refused := (Risk{}).Decide(calmBounds(), failing)
	if refused.Action != ActionInfeasible || refused.Reasons[0].Code != codeRepairStorageFailing {
		t.Fatalf("two failures without conditions gave %q / %v", refused.Action, refused.Reasons)
	}
}

// TestTheSameSnapshotDecidesTwice: a decision is a pure function of the snapshot.
func TestTheSameSnapshotDecidesTwice(t *testing.T) {
	s := report(calmSnapshot(), "store.low-space", boolean(true))
	first := (Risk{}).Decide(calmBounds(), s)
	for i := 0; i < 10; i++ {
		if got := (Risk{}).Decide(calmBounds(), s); !reflect.DeepEqual(got, first) {
			t.Fatalf("the same snapshot decided differently on call %d", i)
		}
	}
	if !strings.Contains(first.Why, "store.low-space") {
		t.Fatalf("why = %q, want the condition named", first.Why)
	}
}

// An incomplete copy is a finer kind of broken (0026): the bytes are whole and the work cannot
// continue. The answer is one write — the only one that can add what is missing — and then a stop,
// because a writer that keeps producing the same incomplete copy is never going to add it.
func TestAnIncompleteCopyIsRepairedOnceThenReported(t *testing.T) {
	s := Snapshot{Copies: []Copy{{Step: 100, State: CopySealed, Missing: []string{"optimizer"}}}}

	first := (Risk{}).Decide(calmBounds(), s)
	if first.Action != ActionRepair || !first.SaveNow {
		t.Fatalf("first answer = %s (saveNow %v), want repair+saveNow", first.Action, first.SaveNow)
	}
	if !strings.Contains(first.Why, "optimizer") {
		t.Fatalf("why = %q, want the missing state named", first.Why)
	}

	s.IncompleteRepairs = 1
	second := (Risk{}).Decide(calmBounds(), s)
	if second.Action != ActionInfeasible {
		t.Fatalf("second answer = %s, want infeasible", second.Action)
	}
	if !strings.Contains(second.Why, "rewrite did not add it") {
		t.Fatalf("why = %q, want the rewrite tried and failed", second.Why)
	}
}
