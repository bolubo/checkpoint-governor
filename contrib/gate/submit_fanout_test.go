package main

// The fanout end to end, without a cluster: a two-node world, two governed units, a two-pod
// arrival, and two stand-in controllers that seal when they see the reclaim. The fake client gives
// the gate the two pod sizes and somewhere to create the job; everything else is the real code path.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// fanoutWorld writes a two-node world where each node is 100m short of the arrival's one pod, and
// the queue has room; only the node gaps justify a reclaim.
func fanoutWorld(t *testing.T, root, cluster string, unitIDs []string, nodes []adapters.NodeState) {
	t.Helper()
	s := adapters.WorldSummary{
		Schema: 1, Cluster: cluster, At: time.Now(), StaleAfterSeconds: 600,
		Nodes:  nodes,
		Queues: []adapters.QueueWire{{Name: "lab", FreeMilli: 800, CapacityMilli: 2000, Allowed: true}},
	}
	if err := adapters.WriteSummary(root, s); err != nil {
		t.Fatal(err)
	}
	for i, id := range unitIDs {
		u := adapters.UnitRecord{
			Schema: 1, Cluster: cluster, UnitID: id, At: time.Now(),
			Node: nodes[i].Name, Priority: 50,
		}
		if err := adapters.WriteUnit(root, cluster, u); err != nil {
			t.Fatal(err)
		}
	}
}

// ckptFixture makes a directory the gate can find by unit id (state.json) with one old checkpoint,
// so a seal has to be a manifest that appears after the hold.
func ckptFixture(t *testing.T, root, unitID string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.Base(unitID))
	if err := os.MkdirAll(filepath.Join(dir, ".ckpt-governor"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"schema":2,"unitId":%q}`, unitID)
	if err := os.WriteFile(filepath.Join(dir, ".ckpt-governor", "state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(dir, "step-0001")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(seed, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"step":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(manifest, old, old); err != nil {
		t.Fatal(err)
	}
	return dir
}

// sealWhenHeld is the victim's controller, reduced to the one behaviour the gate watches: when the
// reclaim for this unit appears, write the checkpoint the gate bought time for — shards and a
// manifest the verifier can read back, not an empty marker.
func sealWhenHeld(worldDir, cluster, unitID, dir string, step int64) {
	path := filepath.Join(worldDir, cluster, "reclaims", adapters.ReclaimFileName(unitID))
	for i := 0; i < 400; i++ {
		if _, err := os.Stat(path); err == nil {
			_ = writeSeal(dir, step, step, "rank-0.bin")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func podFor(name string, cpu string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(cpu),
			}},
		}}},
	}
}

func twoTightNodes() []adapters.NodeState {
	return []adapters.NodeState{
		{Name: "worker-1", Allocatable: cpu(1000), Requested: cpu(1000)},
		{Name: "worker-2", Allocatable: cpu(1000), Requested: cpu(1000)},
	}
}

// A two-pod arrival on two short nodes holds one victim per node and waits for both to seal.
func TestFanoutHoldsOneVictimPerNodeAndWaitsForBoth(t *testing.T) {
	world := t.TempDir()
	ckpt := t.TempDir()
	cluster := "a"
	units := []string{"default/g3-victim-a", "default/g3-victim-b"}
	nodes := twoTightNodes()
	fanoutWorld(t, world, cluster, units, nodes)

	dirs := map[string]string{}
	for _, id := range units {
		dirs[id] = ckptFixture(t, ckpt, id)
	}
	go sealWhenHeld(world, cluster, units[0], dirs[units[0]], 2)
	go sealWhenHeld(world, cluster, units[1], dirs[units[1]], 3)

	client := fake.NewSimpleClientset(podFor("g3-victim-a", "100m"), podFor("g3-victim-b", "100m"))
	job := jobWith("100m", "", 500)
	job.Namespace, job.Name = "default", "g3-arrival"
	job.Spec.Parallelism = ptr32(2)

	var out bytes.Buffer
	code := submitArrival(context.Background(), client, job, 500, submitOptions{
		WorldDir: world, Cluster: cluster, Queue: "lab", CkptRoot: ckpt, Deadline: 15 * time.Second,
	}, &out)
	if code != 0 {
		t.Fatalf("submit = %d: %s", code, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "worker-1") || !strings.Contains(got, "worker-2") {
		t.Fatalf("both nodes must be named: %s", got)
	}
	if !strings.Contains(got, "sealed at step") {
		t.Fatalf("the gate must say it waited: %s", got)
	}
	if !strings.Contains(got, "g3-victim-a (worker-1") || !strings.Contains(got, "g3-victim-b (worker-2") {
		t.Fatalf("one victim per node must be named: %s", got)
	}

	for _, id := range units {
		rec, err := adapters.ReadReclaim(world, cluster, id)
		if err != nil {
			t.Fatalf("reclaim %s: %v", id, err)
		}
		if rec.State != adapters.ReclaimReleased {
			t.Fatalf("reclaim %s = %s, want released", id, rec.State)
		}
		u, err := adapters.ReadUnit(world, cluster, id)
		if err != nil {
			t.Fatalf("unit %s: %v", id, err)
		}
		if u.Permit == nil || u.Permit.Verdict != string(permit.Granted) {
			t.Fatalf("unit %s permit = %+v, want granted", id, u.Permit)
		}
	}

	if _, err := client.BatchV1().Jobs("default").Get(context.Background(), "g3-arrival", metav1.GetOptions{}); err != nil {
		t.Fatalf("the job was not created: %v", err)
	}
}

// A two-pod workload produces one hold, under the workload's id, with both pods named. Holding each
// pod separately would ask for the same write twice and release the arrival after the first (0027).
func TestATwoPodWorkloadProducesOneHold(t *testing.T) {
	world := t.TempDir()
	ckpt := t.TempDir()
	cluster := "a"
	units := []string{"default/train-a-rank0", "default/train-a-rank1"}
	if err := fanoutWorldWithWorkload(t, world, cluster, units, "default/train-a", twoTightNodes()); err != nil {
		t.Fatal(err)
	}
	dir := ckptFixture(t, ckpt, units[0])
	// rank 1 shares rank 0's root: only the leader publishes a unit state there.
	writeSeal(dir, 7, 7, "rank-0.bin", "rank-1.bin")

	client := fake.NewSimpleClientset(podFor("train-a-rank0", "100m"), podFor("train-a-rank1", "100m"))
	job := jobWith("100m", "", 500)
	job.Namespace, job.Name = "default", "arrival"

	var out bytes.Buffer
	code := submitArrival(context.Background(), client, job, 500, submitOptions{
		WorldDir: world, Cluster: cluster, Queue: "lab", CkptRoot: ckpt, Deadline: 10 * time.Second,
	}, &out)
	if code != 0 {
		t.Fatalf("submit = %d: %s", code, out.String())
	}
	got := out.String()
	if strings.Count(got, "default/train-a (") != 1 {
		t.Fatalf("the workload must be named once: %s", got)
	}
	entries, err := os.ReadDir(filepath.Join(world, cluster, "reclaims"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("reclaims = %d, want one hold for the workload: %v", len(entries), entries)
	}
	rec, err := adapters.ReadReclaim(world, cluster, "default/train-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Pods) != 2 {
		t.Fatalf("the hold must name both pods: %+v", rec.Pods)
	}
}

// A seal that names one rank of a two-pod workload is not the workload's copy: the arrival waits
// until the second rank's shard is in the manifest (0027).
func TestATwoPodWorkloadWaitsForEveryRank(t *testing.T) {
	world := t.TempDir()
	ckpt := t.TempDir()
	cluster := "a"
	units := []string{"default/train-b-rank0", "default/train-b-rank1"}
	if err := fanoutWorldWithWorkload(t, world, cluster, units, "default/train-b", twoTightNodes()); err != nil {
		t.Fatal(err)
	}
	dir := ckptFixture(t, ckpt, units[0])

	client := fake.NewSimpleClientset(podFor("train-b-rank0", "100m"), podFor("train-b-rank1", "100m"))
	job := jobWith("100m", "", 500)
	job.Namespace, job.Name = "default", "arrival-b"

	var out bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- submitArrival(context.Background(), client, job, 500, submitOptions{
			WorldDir: world, Cluster: cluster, Queue: "lab", CkptRoot: ckpt, Deadline: 10 * time.Second,
		}, &out)
	}()

	// Wait for the hold, then seal half the workload: the gate must keep waiting.
	waitForReclaim(t, world, cluster, "default/train-b")
	if err := writeSeal(dir, 7, 7, "rank-0.bin"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := client.BatchV1().Jobs("default").Get(context.Background(), "arrival-b", metav1.GetOptions{}); err == nil {
		t.Fatal("the arrival was created on a half seal")
	}
	// The second rank lands: the workload is now whole.
	if err := writeSeal(dir, 8, 8, "rank-0.bin", "rank-1.bin"); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 0 {
		t.Fatalf("submit = %d: %s", code, out.String())
	}
	if _, err := client.BatchV1().Jobs("default").Get(context.Background(), "arrival-b", metav1.GetOptions{}); err != nil {
		t.Fatalf("the arrival was not created after the full seal: %v", err)
	}
	if !strings.Contains(out.String(), "rank 1") {
		t.Fatalf("the wait must name the missing rank: %s", out.String())
	}
}

// The deadline is fail-open, but the record must say the write was asked for and not covered.
func TestATimeoutMarksTheHoldUncovered(t *testing.T) {
	world := t.TempDir()
	ckpt := t.TempDir()
	cluster := "a"
	units := []string{"default/g4-victim"}
	fanoutWorld(t, world, cluster, units, twoTightNodes())
	ckptFixture(t, ckpt, units[0])

	client := fake.NewSimpleClientset(podFor("g4-victim", "100m"))
	job := jobWith("100m", "", 500)
	job.Namespace, job.Name = "default", "arrival-uncovered"

	var out bytes.Buffer
	code := submitArrival(context.Background(), client, job, 500, submitOptions{
		WorldDir: world, Cluster: cluster, Queue: "lab", CkptRoot: ckpt, Deadline: 300 * time.Millisecond,
	}, &out)
	if code != 0 {
		t.Fatalf("submit = %d: %s", code, out.String())
	}
	rec, err := adapters.ReadReclaim(world, cluster, units[0])
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != adapters.ReclaimUncovered {
		t.Fatalf("a timeout state = %s, want uncovered: %s", rec.State, out.String())
	}
}

// A multi-pod workload whose checkpoint root is a per-node hostPath has no shared copy to wait for.
func TestAPerNodeVolumeIsRefused(t *testing.T) {
	ckpt := t.TempDir()
	pods := []corev1.Pod{*podWithHostPath("train-c-rank0", "100m", ckpt, "/g1"),
		*podWithHostPath("train-c-rank1", "100m", ckpt, "/g1")}
	if pod, host := perNodeCheckpoint(pods, []string{"default/train-c-rank0", "default/train-c-rank1"}, ckpt); pod == "" || host != "/g1" {
		t.Fatalf("a hostPath at the checkpoint root must be found: %q %q", pod, host)
	}

	world := t.TempDir()
	cluster := "a"
	units := []string{"default/train-c-rank0", "default/train-c-rank1"}
	if err := fanoutWorldWithWorkload(t, world, cluster, units, "default/train-c", twoTightNodes()); err != nil {
		t.Fatal(err)
	}
	ckptFixture(t, ckpt, units[0])

	client := fake.NewSimpleClientset(&pods[0], &pods[1])
	job := jobWith("100m", "", 500)
	job.Namespace, job.Name = "default", "arrival-c"

	var out bytes.Buffer
	if code := submitArrival(context.Background(), client, job, 500, submitOptions{
		WorldDir: world, Cluster: cluster, Queue: "lab", CkptRoot: ckpt, Deadline: 10 * time.Second,
	}, &out); code != 2 {
		t.Fatalf("submit = %d, want a refusal: %s", code, out.String())
	}
	if _, err := client.BatchV1().Jobs("default").Get(context.Background(), "arrival-c", metav1.GetOptions{}); err == nil {
		t.Fatal("a refused workload must not be created")
	}
}

// fanoutWorldWithWorkload writes units that all name one workload, the shape the observer produces
// for a multi-pod Job.
func fanoutWorldWithWorkload(t *testing.T, root, cluster string, unitIDs []string, workload string, nodes []adapters.NodeState) error {
	t.Helper()
	s := adapters.WorldSummary{
		Schema: 1, Cluster: cluster, At: time.Now(), StaleAfterSeconds: 600,
		Nodes:  nodes,
		Queues: []adapters.QueueWire{{Name: "lab", FreeMilli: 800, CapacityMilli: 2000, Allowed: true}},
	}
	if err := adapters.WriteSummary(root, s); err != nil {
		return err
	}
	for _, id := range unitIDs {
		u := adapters.UnitRecord{
			Schema: 1, Cluster: cluster, UnitID: id, At: time.Now(),
			Node: nodes[0].Name, Priority: 50, Workload: workload, WorkloadKind: "Job",
		}
		if err := adapters.WriteUnit(root, cluster, u); err != nil {
			return err
		}
	}
	return nil
}

func podWithHostPath(name, cpu, mountPath, hostPath string) *corev1.Pod {
	p := podFor(name, cpu)
	p.Spec.Volumes = []corev1.Volume{{
		Name:         "ckpt",
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: hostPath}},
	}}
	p.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{
		Name: "ckpt", MountPath: mountPath,
	}}
	return p
}

// waitForReclaim waits until the gate has written the hold. A test that writes a seal before the
// reclaim would be testing its own clock, not the gate's.
func waitForReclaim(t *testing.T, worldDir, cluster, unitID string) {
	t.Helper()
	path := filepath.Join(worldDir, cluster, "reclaims", adapters.ReclaimFileName(unitID))
	for i := 0; i < 400; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the gate never wrote a reclaim for %s", unitID)
}

// All-or-nothing: worker-2 has nobody to ask, so the gate does not hold at all and submits directly.
func TestFanoutIsAllOrNothingWhenOneNodeHasNoVictim(t *testing.T) {
	world := t.TempDir()
	ckpt := t.TempDir()
	cluster := "a"
	nodes := twoTightNodes()
	fanoutWorld(t, world, cluster, []string{"default/g3-victim-a"}, nodes)
	dir := ckptFixture(t, ckpt, "default/g3-victim-a")
	go sealWhenHeld(world, cluster, "default/g3-victim-a", dir, 2)

	client := fake.NewSimpleClientset(podFor("g3-victim-a", "100m"))
	job := jobWith("100m", "", 500)
	job.Namespace, job.Name = "default", "g3-arrival"
	job.Spec.Parallelism = ptr32(2)

	var out bytes.Buffer
	code := submitArrival(context.Background(), client, job, 500, submitOptions{
		WorldDir: world, Cluster: cluster, Queue: "lab", CkptRoot: ckpt, Deadline: 15 * time.Second,
	}, &out)
	if code != 0 {
		t.Fatalf("submit = %d: %s", code, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "worker-2") || !strings.Contains(got, "submitted") {
		t.Fatalf("the refusal must name worker-2 and still submit: %s", got)
	}
	if _, err := adapters.ReadReclaim(world, cluster, "default/g3-victim-a"); err == nil {
		t.Fatal("a partial hold was written; all-or-nothing says no hold")
	}
}
