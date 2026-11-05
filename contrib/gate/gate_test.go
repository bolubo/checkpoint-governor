package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// writeSeal writes a contract-shaped checkpoint the verifier can judge: shards first, then a manifest
// that names them with sizes and tail hashes. The gate's own tests must produce copies the real
// verifier accepts, otherwise the fixture proves a rule the product does not have.
func writeSeal(dir string, generation, step int64, shardNames ...string) error {
	gen := filepath.Join(dir, fmt.Sprintf("gen-%04d", generation))
	if err := os.MkdirAll(gen, 0o755); err != nil {
		return err
	}
	var entries []string
	for i, name := range shardNames {
		data := []byte(fmt.Sprintf("rank %d, step %d", i, step))
		if err := os.WriteFile(filepath.Join(gen, name), data, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		entries = append(entries, fmt.Sprintf(
			`{"name":%q,"size":%d,"sha256":%q,"tailSha256":%q,"member":%d}`,
			name, len(data), hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:]), i))
	}
	manifest := fmt.Sprintf(`{"generation":%d,"step":%d,"sealedAt":%q,"writer":"fixture","shards":[%s]}`,
		generation, step, time.Now().UTC().Format(time.RFC3339), joinStrings(entries, ","))
	path := filepath.Join(gen, "manifest.json")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		return err
	}
	// The hold is a *time*: a seal that is already there is not evidence the victim heard us.
	at := time.Now()
	return os.Chtimes(path, at, at)
}

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

// parallelism is the pod count; zero or absent is one. Completions index pods one after another, so
// they do not add a second concurrent request.
func TestPodsOfReadsParallelism(t *testing.T) {
	j := jobWith("2000m", "", 100)
	if got := podsOf(j); got != 1 {
		t.Fatalf("pods = %d, want 1 without parallelism", got)
	}
	j.Spec.Parallelism = ptr32(2)
	if got := podsOf(j); got != 2 {
		t.Fatalf("pods = %d, want 2", got)
	}
	j.Spec.Parallelism = ptr32(0)
	if got := podsOf(j); got != 1 {
		t.Fatalf("pods = %d, want 1 for an explicit zero", got)
	}
}

// pickNodes spreads the pods over the emptiest readable nodes and skips the ones it cannot measure.
func TestPickNodesSpreadsThePods(t *testing.T) {
	nodes := []adapters.NodeState{
		{Name: "worker-1", Allocatable: cpu(4000), Requested: cpu(1000)},
		{Name: "worker-2", Allocatable: cpu(4000), Requested: cpu(3000)},
		{Name: "worker-3", Allocatable: cpu(4000), Requested: cpu(1000), Cordoned: true},
		{Name: "worker-4", Allocatable: cpu(4000), Requested: cpu(1000), RequestsUnknown: true},
	}
	if got := pickNodes(nodes, 1); len(got) != 1 || got[0].Name != "worker-1" || got[0].Pods != 1 {
		t.Fatalf("one pod = %+v, want worker-1 with one pod", got)
	}
	if got := pickNodes(nodes, 2); len(got) != 2 || got[0].Name != "worker-1" || got[1].Name != "worker-2" ||
		got[0].Pods != 1 || got[1].Pods != 1 {
		t.Fatalf("two pods = %+v, want one on each of worker-1/worker-2", got)
	}
	short := []adapters.NodeState{
		{Name: "worker-1", Allocatable: cpu(4000), Requested: cpu(1000)},
		{Name: "worker-2", Allocatable: cpu(4000), Requested: cpu(3000)},
	}
	if got := pickNodes(short, 3); len(got) != 2 || got[0].Pods != 2 || got[1].Pods != 1 {
		t.Fatalf("three pods on two nodes = %+v, want 2+1", got)
	}
	if got := pickNodes([]adapters.NodeState{{Name: "w", Cordoned: true}}, 1); got != nil {
		t.Fatalf("cordoned node = %+v, want none", got)
	}
}

// A node gap is per node: room freed on worker-1 does not hold a pod that must land on worker-2.
func TestShortfallPerNodeIsPerNode(t *testing.T) {
	picks := []nodePick{
		{Name: "worker-1", FreeMilli: 3000, Pods: 1},
		{Name: "worker-2", FreeMilli: 3000, Pods: 1},
	}
	got := shortfallPerNode(picks, 4000)
	if len(got) != 2 || got[0] != 1000 || got[1] != 1000 {
		t.Fatalf("shortfall = %v, want [1000 1000]", got)
	}
}

// The queue is a gate, not the whole arithmetic: the node gaps are what the victims must cover, and
// the queue's gap is the floor of the total.
func TestPlanRoomKeepsTheNodeGaps(t *testing.T) {
	nodes := []adapters.NodeState{
		{Name: "worker-1", Allocatable: cpu(4000), Requested: cpu(3000)},
		{Name: "worker-2", Allocatable: cpu(4000), Requested: cpu(3000)},
	}
	q := adapters.QueueWire{Name: "lab", FreeMilli: 4000, CapacityMilli: 4000, Allowed: true}
	plan, hold, why := planRoom(nodes, q, 2000, 2, nil)
	if !hold {
		t.Fatalf("not holding: %s", why)
	}
	if plan.Total != 4000 || plan.QueueShort != 0 || len(plan.NodeShort) != 2 ||
		plan.NodeShort[0] != 1000 || plan.NodeShort[1] != 1000 || plan.Want != 2000 {
		t.Fatalf("plan = %+v, want queue 0, node gaps 1000+1000, want 2000", plan)
	}

	q2 := adapters.QueueWire{Name: "lab", FreeMilli: 1000, CapacityMilli: 4000, Allowed: true}
	plan, hold, _ = planRoom(nodes, q2, 2000, 2, nil)
	if !hold || plan.QueueShort != 3000 || plan.Want != 3000 {
		t.Fatalf("queue gap = %+v, want queue 3000 and want 3000", plan)
	}

	q3 := adapters.QueueWire{Name: "lab", FreeMilli: 500, CapacityMilli: 4000,
		Cohort: "c", CohortFreeMilli: 3000, Allowed: true}
	plan, hold, _ = planRoom(nodes, q3, 2000, 2, nil)
	if !hold || plan.QueueShort != 1000 {
		t.Fatalf("cohort room = %+v, want the cohort's 3000 to leave 1000", plan)
	}
}

// The reasons a gate does not wait: the queue will not preempt, the work can never fit, the arrival
// already fits, the pinned node is unknown, or there is nothing to ask for.
func TestPlanRoomRefusesWhatIsNotAReclaim(t *testing.T) {
	nodes := []adapters.NodeState{
		{Name: "worker-1", Allocatable: cpu(4000), Requested: cpu(3000)},
		{Name: "worker-2", Allocatable: cpu(4000), Requested: cpu(3000)},
	}
	check := func(name string, q adapters.QueueWire, perPod int64, pods int, pinned []string) {
		t.Helper()
		if _, hold, why := planRoom(nodes, q, perPod, pods, pinned); hold || why == "" {
			t.Fatalf("%s: hold=%v why=%q, want no hold and a sentence", name, hold, why)
		}
	}
	check("a queue that may not preempt", adapters.QueueWire{Name: "lab", FreeMilli: 0, Allowed: false}, 2000, 2, nil)
	check("work past capacity", adapters.QueueWire{Name: "lab", CapacityMilli: 500, Allowed: true}, 2000, 2, nil)
	check("the arrival fits", adapters.QueueWire{Name: "lab", FreeMilli: 4000, CapacityMilli: 4000, Allowed: true}, 1000, 1, nil)
	check("an unknown pinned node", adapters.QueueWire{Name: "lab", FreeMilli: 0, Allowed: true}, 2000, 2, []string{"nope"})
	check("nothing asked for", adapters.QueueWire{Name: "lab", FreeMilli: 0, Allowed: true}, 0, 1, nil)
}

// One victim per short node, never two from the node with the cheaper units.
func TestPickVictimsTakesOnePerShortNode(t *testing.T) {
	plan := roomPlan{
		Picks:     []nodePick{{Name: "worker-1", FreeMilli: 3000, Pods: 1}, {Name: "worker-2", FreeMilli: 3000, Pods: 1}},
		NodeShort: []int64{1000, 1000}, Want: 2000, Total: 4000, Pods: 2, PerPod: 2000,
	}
	units := []adapters.UnitRecord{
		{UnitID: "team/a", Node: "worker-1", Priority: 100},
		{UnitID: "team/b", Node: "worker-2", Priority: 100},
	}
	sizes := map[string]int64{"team/a": 2000, "team/b": 2000}
	dirs := map[string]string{"team/a": "/ckpt/a", "team/b": "/ckpt/b"}
	got, why := pickVictims(plan, units, sizes, dirs, 500)
	if why != "" || len(got) != 2 || got[0].ID != "team/a" || got[1].ID != "team/b" {
		t.Fatalf("victims = %+v why=%q, want team/a then team/b", got, why)
	}

	// All-or-nothing: worker-2 has nobody to ask, so the gate does not hold at all.
	got, why = pickVictims(plan, units[:1], sizes, map[string]string{"team/a": "/ckpt/a"}, 500)
	if got != nil || why == "" {
		t.Fatalf("victims = %+v why=%q, want no hold", got, why)
	}
}

// When the nodes have room but the queue does not, the queue gap is topped up from any selected node.
func TestPickVictimsTopsUpTheQueueGap(t *testing.T) {
	plan := roomPlan{
		Picks: []nodePick{{Name: "worker-1", FreeMilli: 2000, Pods: 1}, {Name: "worker-2", FreeMilli: 2000, Pods: 1}},
		Total: 4000, Pods: 2, PerPod: 2000,
		QueueShort: 3000, NodeShort: []int64{0, 0}, Want: 3000,
	}
	units := []adapters.UnitRecord{
		{UnitID: "team/a", Node: "worker-1", Priority: 100},
		{UnitID: "team/b", Node: "worker-2", Priority: 100},
	}
	sizes := map[string]int64{"team/a": 2000, "team/b": 2000}
	dirs := map[string]string{"team/a": "/ckpt/a", "team/b": "/ckpt/b"}
	got, why := pickVictims(plan, units, sizes, dirs, 500)
	if why != "" || len(got) != 2 {
		t.Fatalf("top-up = %+v why=%q, want both units", got, why)
	}

	// One unit of 1000m cannot free the 3000m the queue is short: no hold.
	got, why = pickVictims(plan, units[:1], map[string]int64{"team/a": 1000}, map[string]string{"team/a": "/ckpt/a"}, 500)
	if got != nil || why == "" {
		t.Fatalf("short top-up = %+v why=%q, want no hold", got, why)
	}
}

// The single-pod, single-node line is the one every earlier experiment printed; it must not change.
func TestGateSentenceKeepsTheSingleNodeLine(t *testing.T) {
	job := jobWith("4000m", "", 100)
	job.Namespace, job.Name = "default", "train"
	plan := roomPlan{Picks: []nodePick{{Name: "worker-1"}}, Pods: 1, Total: 4000, Want: 2000}
	want := "default/train needs 4000m; lab is 2000m short on worker-1"
	if got := gateSentence(job, plan, "lab"); got != want {
		t.Fatalf("gate line = %q, want %q", got, want)
	}

	plan.Picks = []nodePick{{Name: "worker-1", Pods: 1}, {Name: "worker-2", Pods: 1}}
	plan.Pods, plan.NodeShort, plan.QueueShort, plan.Want = 2, []int64{1000, 1000}, 0, 2000
	want = "default/train needs 4000m over 2 pods; worker-1 is 1000m short; worker-2 is 1000m short"
	if got := gateSentence(job, plan, "lab"); got != want {
		t.Fatalf("multi gate line = %q, want %q", got, want)
	}
}

func cpu(milli int64) adapters.Resources { return adapters.Resources{CPUMilli: milli} }

func ptr32(v int32) *int32 { return &v }

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

// A seal the gate releases on has to be one a restore could read: hiding one rank's shard must stop
// the release and name the shard, and putting it back must let the release through (0027).
func TestAVerifiedSealRequiresTheShards(t *testing.T) {
	dir := t.TempDir()
	if err := writeSeal(dir, 7, 700, "rank-0.bin", "rank-1.bin"); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Second)

	step, ok, problem := newestVerifiedSeal(dir, since, 2)
	if !ok || step != 700 || problem != "" {
		t.Fatalf("a whole copy must verify: ok=%v step=%d problem=%q", ok, step, problem)
	}

	if err := os.Remove(filepath.Join(dir, "gen-0007", "rank-1.bin")); err != nil {
		t.Fatal(err)
	}
	if _, ok, problem := newestVerifiedSeal(dir, since, 2); ok {
		t.Fatal("a copy with a hidden shard must not release the arrival")
	} else if !strings.Contains(problem, "rank-1.bin") {
		t.Fatalf("the refusal must name the shard: %q", problem)
	}

	if err := writeSeal(dir, 7, 700, "rank-0.bin", "rank-1.bin"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := newestVerifiedSeal(dir, since, 2); !ok {
		t.Fatal("a restored copy must verify again")
	}
}

// A seal that names one rank of a two-pod workload is half a copy: the arrival must wait for the
// second rank, and the line must say which one is missing (0027).
func TestASealMustNameEveryRankOfAWorkload(t *testing.T) {
	dir := t.TempDir()
	if err := writeSeal(dir, 9, 900, "rank-0.bin"); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Second)
	if _, ok, problem := newestVerifiedSeal(dir, since, 2); ok {
		t.Fatal("a one-rank seal must not release a two-pod workload")
	} else if !strings.Contains(problem, "rank 1") {
		t.Fatalf("the refusal must name the missing rank: %q", problem)
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
