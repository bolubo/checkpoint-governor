package adapters

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/produce"
)

func writeHeld(t *testing.T, dir, unitID, state string, since, deadline time.Time) {
	t.Helper()
	if err := WriteReclaim(dir, "a", ReclaimRecord{
		Cluster: "a", UnitID: unitID, RequestID: "r-1", Intruder: "intruder",
		State: state, Since: since, Deadline: deadline,
	}); err != nil {
		t.Fatal(err)
	}
}

// The gate's record is the whole point of decision 0030: a held reclaim is an announcement with lead
// time, so `risk.preempt` fires without the platform having written a deletion timestamp yet.
func TestHeldReclaimIsAnAnnouncement(t *testing.T) {
	dir, now := projectFixture(t, 10*time.Second)
	writeHeld(t, dir, "team/train-0", ReclaimHeld, now, now.Add(5*time.Minute))

	v, err := Project(dir, "a", "team/train-0", Deployment{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Announced {
		t.Fatalf("a held reclaim did not announce: %+v", v)
	}
	if v.Reclaim == nil || v.Reclaim.RequestID != "r-1" {
		t.Fatalf("the record behind the announcement was lost: %+v", v.Reclaim)
	}
	got := judgePreemption(v, now, true)
	if got.State != produce.Fresh || !got.Value {
		t.Fatalf("risk.preempt = %s/%v, want fresh/1", got.State, got.Value)
	}
}

// Every record that no longer stops an arrival must not announce: a released hold is over, an
// uncovered one was never kept, and a deadline that passed is the fail-open edge.
func TestReclaimStatesThatAreNotHeldDoNotAnnounce(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		deadline time.Time
	}{
		{"released", ReclaimReleased, time.Time{}},
		{"uncovered", ReclaimUncovered, time.Time{}},
		{"held but expired", ReclaimHeld, time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, now := projectFixture(t, 10*time.Second)
			deadline := c.deadline
			if c.name == "held but expired" {
				deadline = now.Add(-time.Second)
			}
			writeHeld(t, dir, "team/train-0", c.state, now, deadline)

			v, err := Project(dir, "a", "team/train-0", Deployment{}, now)
			if err != nil {
				t.Fatal(err)
			}
			if v.Announced || v.Reclaim != nil {
				t.Fatalf("%s still announced: %+v", c.name, v)
			}
			if got := judgePreemption(v, now, true); got.State != produce.Fresh || got.Value {
				t.Fatalf("%s: risk.preempt = %s/%v, want fresh/0", c.name, got.State, got.Value)
			}
		})
	}
}

// A hold is about this unit by name, so it survives a world that does not place the unit yet: the gate
// decided before the platform wrote anything, and losing that would be blindness about a fact we hold.
func TestAHoldIsReadEvenWhenTheUnitIsNotPlaced(t *testing.T) {
	dir, now := projectFixture(t, 10*time.Second)
	if err := os.Remove(filepath.Join(dir, "a", unitsDirName, UnitFileName("team/train-0"))); err != nil {
		t.Fatal(err)
	}
	writeHeld(t, dir, "team/train-0", ReclaimHeld, now, now.Add(time.Minute))

	v, err := Project(dir, "a", "team/train-0", Deployment{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Announced || v.Reclaim == nil {
		t.Fatalf("an unplaced unit lost its hold: %+v", v)
	}
	if got := judgePreemption(v, now, false); got.State != produce.Fresh || !got.Value {
		t.Fatalf("risk.preempt = %s/%v, want fresh/1", got.State, got.Value)
	}
}

// A corrupt record is an error, not silence: a world we were told to read and cannot is the one answer
// that must never look calm.
func TestACorruptReclaimFailsTheProjection(t *testing.T) {
	dir, now := projectFixture(t, 10*time.Second)
	path := filepath.Join(dir, "a", reclaimsDirName, ReclaimFileName("team/train-0"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Project(dir, "a", "team/train-0", Deployment{}, now); err == nil {
		t.Fatal("a corrupt reclaim projected to silence")
	}
}
