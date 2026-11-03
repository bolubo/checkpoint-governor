package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

func jobWith(cpu string, initCPU string, priority int32) *batchv1.Job {
	j := &batchv1.Job{}
	j.Spec.Template.Spec.Priority = &priority
	c := corev1.Container{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}}}
	if cpu != "" {
		c.Resources.Requests[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	j.Spec.Template.Spec.Containers = []corev1.Container{c}
	if initCPU != "" {
		ic := corev1.Container{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}}}
		ic.Resources.Requests[corev1.ResourceCPU] = resource.MustParse(initCPU)
		j.Spec.Template.Spec.InitContainers = []corev1.Container{ic}
	}
	return j
}

// The arrival's request is the scheduler's rule: containers summed, init as a floor.
func TestNeedOfSumsContainersAndTakesTheLargestInit(t *testing.T) {
	if got := needOf(jobWith("2", "3", 100)); got != 3000 {
		t.Fatalf("need = %d, want 3000 (max(2, init 3))", got)
	}
	if got := needOf(jobWith("2", "1", 100)); got != 2000 {
		t.Fatalf("need = %d, want 2000", got)
	}
	if got := needOf(jobWith("", "", 100)); got != 0 {
		t.Fatalf("a job asking for nothing needs %d", got)
	}
}

// Four different reasons a gate must not wait: the queue will not preempt, the work can never fit,
// the arrival already fits, or there is nothing to ask for.
func TestShortfallForRefusesCasesThatAreNotReclaims(t *testing.T) {
	base := adapters.QueueWire{Name: "lab", FreeMilli: 2000, CapacityMilli: 4000, Allowed: true}
	cases := []struct {
		name     string
		q        adapters.QueueWire
		need     int64
		nodeFree int64
		none     bool
		want     int64
	}{
		{"a shortfall", base, 4000, 4000, false, 2000},
		{"the arrival fits", base, 1000, 1000, false, 0},
		{"the queue may not preempt", adapters.QueueWire{Name: "lab", FreeMilli: 0, Allowed: false}, 4000, 4000, true, 0},
		{"the work can never fit", adapters.QueueWire{Name: "lab", CapacityMilli: 500, Allowed: true}, 4000, 4000, true, 0},
		{"nothing asked for", base, 0, 4000, true, 0},
	}
	for _, c := range cases {
		got, pressing := shortfallFor(c.q, c.nodeFree, c.need)
		if c.none || c.want == 0 {
			if pressing || got != 0 {
				t.Errorf("%s: shortfall = %d/%v, want no hold", c.name, got, pressing)
			}
			continue
		}
		if !pressing || got != c.want {
			t.Errorf("%s: shortfall = %d/%v, want %d", c.name, got, pressing, c.want)
		}
	}
}

// A cohort's room is the group's, and the node's room caps it: a reclaim can only free what the node
// holds. This is the measured rule's two terms (0023) applied to an arrival the world has not seen.
func TestShortfallForUsesTheCohortAndTheNode(t *testing.T) {
	q := adapters.QueueWire{Name: "lab", FreeMilli: 500, CapacityMilli: 4000,
		Cohort: "c", CohortFreeMilli: 3000, Allowed: true}
	if got, pressing := shortfallFor(q, 3000, 4000); !pressing || got != 1000 {
		t.Fatalf("cohort room: shortfall = %d/%v, want 1000", got, pressing)
	}
	if got, pressing := shortfallFor(q, 1000, 4000); !pressing || got != 3000 {
		t.Fatalf("node room caps the cohort: shortfall = %d/%v, want 3000", got, pressing)
	}
}

// Only a unit that is below the arrival, on the chosen node, and whose size is known can be asked to
// pay. The last clause matters: a hold on an unknown size is a guess, not a selection.
func TestCandidatesArePlacedLowerPriorityAndSized(t *testing.T) {
	units := []adapters.UnitRecord{
		{UnitID: "team/low", Node: "worker-1", Priority: 100},
		{UnitID: "team/same", Node: "worker-1", Priority: 500},
		{UnitID: "team/other-node", Node: "worker-2", Priority: 100},
		{UnitID: "team/no-size", Node: "worker-1", Priority: 100},
		{UnitID: "team/unplaced", Node: "", Priority: 100},
		// A system pod the observer recorded: lower priority than everything, and not governed here.
		// It must not be able to outrank and hide the real victim.
		{UnitID: "kueue-system/controller", Node: "worker-1", Priority: 0},
	}
	sizes := map[string]int64{"team/low": 2000, "team/same": 2000, "team/other-node": 2000,
		"kueue-system/controller": 500}
	dirs := map[string]string{"team/low": "/ckpt/low", "team/same": "/ckpt/same", "team/other-node": "/ckpt/other"}
	got := candidates(units, sizes, dirs, "worker-1", 500)
	if len(got) != 1 || got[0].ID != "team/low" || got[0].Size != 2000 || got[0].Priority != 100 {
		t.Fatalf("candidates = %+v, want only team/low", got)
	}
}

// The rank is the gate's, and the same selection the fleet already uses: lowest priority, then the
// largest footprint. Reusing it is the point — there is no second rule to drift.
func TestVictimsComeFromTheSharedRule(t *testing.T) {
	cands := []permit.Unit{
		{ID: "team/a", Priority: 100, Size: 1000},
		{ID: "team/b", Priority: 100, Size: 1000},
		{ID: "team/c", Priority: 50, Size: 1000},
	}
	got := permit.Victims(cands, permit.CauseReclaim, 3000)
	if len(got) != 3 {
		t.Fatalf("victims = %+v, want all three (a tie is never broken)", got)
	}
	got = permit.Victims(cands, permit.CauseReclaim, 1000)
	if len(got) != 1 || got[0].ID != "team/c" {
		t.Fatalf("one core should take the cheapest only: %+v", got)
	}
}

// A seal is a manifest that appeared after the hold. A checkpoint that was already there is not
// evidence the victim heard us.
func TestNewestManifestOnlyCountsASealAfterTheHold(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, step string, at time.Time) {
		sub := filepath.Join(dir, name)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "manifest.json"), []byte(`{"step":`+step+`}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(sub, "manifest.json"), at, at); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	mk("step-10", "10", since.Add(-time.Minute))
	if _, _, ok := newestManifest(dir, since); ok {
		t.Fatal("an older checkpoint counted as a seal")
	}
	mk("gen-0002", "42", since.Add(2*time.Second))
	at, step, ok := newestManifest(dir, since)
	if !ok || step != 42 || at.Before(since) {
		t.Fatalf("newest = %v/%d/%v, want the generation's step 42", at, step, ok)
	}
}

// The gate finds a victim's directory by the unit id the controller publishes, not by guessing a
// name: two units may share a basename in different namespaces.
func TestFindUnitDirUsesThePublishedUnitID(t *testing.T) {
	root := t.TempDir()
	write := func(name, unit string) {
		base := filepath.Join(root, name, ".ckpt-governor")
		if err := os.MkdirAll(base, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "state.json"), []byte(`{"unitId":"`+unit+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha", "one/train")
	write("beta", "two/train")
	if d, ok := findUnitDir(root, "two/train"); !ok || filepath.Base(d) != "beta" {
		t.Fatalf("found %q/%v, want beta", d, ok)
	}
	if _, ok := findUnitDir(root, "three/train"); ok {
		t.Fatal("a unit that is not there was matched")
	}
}
