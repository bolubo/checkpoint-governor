// Package world turns a cluster into the files the controller reads.
//
// The structs here are a **copy of the on-disk contract**, not an import: the observer is its own
// module and cannot reach the core's internal packages. The contract itself is the JSON, and both
// sides test against the same example files under docs/world/examples, so a field renamed on one side
// fails a test on the other instead of silently dropping a fact.
package world

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Resources is one row of the accounting, normalised: milli-cores, bytes, a GPU count. Kubernetes
// hands these out as strings with suffixes, and the conversion belongs here — once — not in every
// consumer.
type Resources struct {
	CPUMilli int64 `json:"cpuMilli"`
	MemBytes int64 `json:"memBytes"`
	GPUCount int64 `json:"gpuCount"`
}

type NodeState struct {
	Name                  string    `json:"name"`
	Allocatable           Resources `json:"allocatable"`
	Requested             Resources `json:"requested"`
	Cordoned              bool      `json:"cordoned"`
	Taints                []string  `json:"taints,omitempty"`
	RequestsUnknown       bool      `json:"requestsUnknown,omitempty"`
	PendingHigherPriority int       `json:"pendingHigherPriority"`
}

type Capability struct {
	Exists bool   `json:"exists"`
	Source string `json:"source,omitempty"`
}

type Summary struct {
	Schema            int                   `json:"schema"`
	Cluster           string                `json:"cluster"`
	At                time.Time             `json:"at"`
	StaleAfterSeconds int                   `json:"staleAfterSeconds"`
	Capabilities      map[string]Capability `json:"capabilities,omitempty"`
	// PendingEvent names the fact "higher-priority work is waiting for room", as one id for the whole
	// cluster. It is a fact about the arrival, not about any unit and not about what the scheduler will
	// do with it (0022): while the same work waits, the id is the same and one write answers it.
	PendingEvent string `json:"pendingEvent,omitempty"`
	// Arrivals is the same fact unaggregated: one entry per job waiting for room or just admitted. The
	// observer publishes facts; the governor, which remembers its last write, decides what is new for it.
	Arrivals []Arrival `json:"arrivals,omitempty"`
	// Queues is what each queue can still admit, and whether it may take it from somebody. The
	// measured rule needs both: `shortfall > 0 AND higher priority waiting AND preemption allowed`.
	Queues []QueueState `json:"queues,omitempty"`
	Nodes  []NodeState  `json:"nodes"`
}

type UnitRecord struct {
	Schema            int        `json:"schema"`
	Cluster           string     `json:"cluster"`
	UnitID            string     `json:"unitId"`
	At                time.Time  `json:"at"`
	Node              string     `json:"node"`
	Priority          int32      `json:"priority"`
	QoS               string     `json:"qos"`
	DeletionTimestamp *time.Time `json:"deletionTimestamp"`
	Evicted           bool       `json:"evicted"`
	// HigherPriorityWaiting counts the jobs the queue is holding back that outrank this unit. Zero means
	// this unit cannot be the one taken to make room, whatever else is true.
	HigherPriorityWaiting int `json:"higherPriorityWaiting,omitempty"`
	// Permit is this unit's permission to spend a write when one cause endangers several units
	// (docs/decisions/0021). It is written here, and only here, because the answer needs the set: a
	// governor that sees one unit cannot tell "I am the victim" from "my neighbour is".
	Permit *Permit `json:"permit,omitempty"`
}

// Permit is the verdict. Absent means permission: a governor reads a missing field as granted, so a
// cluster whose observer predates this field behaves exactly as it did before.
type Permit struct {
	Verdict string `json:"verdict"`
	// Cause is the kind of pressure: reclaim.
	Cause string     `json:"cause,omitempty"`
	Rank  int        `json:"rank,omitempty"`
	Until *time.Time `json:"until,omitempty"`
}

// Snapshot is one read of the cluster.
type Snapshot struct {
	Summary Summary
	Units   []UnitRecord
}

type Options struct {
	Cluster    string
	StaleAfter time.Duration
	// Units decides which pods get a record: "labeled" (only those carrying UnitLabel), "all", or
	// "none" for a summary-only observer.
	Units     string
	UnitLabel string
	// EventWindow is how long an admitted job keeps counting as "just arrived". It has to exceed the
	// decision cadence by a comfortable margin: the suspension that makes a job visible lasts about a
	// second on a preemptible queue.
	EventWindow time.Duration
	// Dynamic reads the queue's own numbers. Nil means the cluster has no queue API (or the caller did
	// not build one), and the summary then simply has no queue section.
	Dynamic dynamic.Interface
}

// Read lists nodes and pods once and folds them into a snapshot.
//
// It is a LIST per interval rather than an informer: a cluster of a few hundred nodes costs one
// request every few seconds, and the code stays short enough to audit. The moment a LIST takes
// longer than the interval, or the cluster grows past a few thousand pods, the same function can be
// fed from an informer cache — the folding below does not know where the lists came from.
func Read(client kubernetes.Interface, opts Options, now time.Time) (Snapshot, error) {
	ctx := context.Background()
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Snapshot{}, fmt.Errorf("list nodes: %w", err)
	}
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return Snapshot{}, fmt.Errorf("list pods: %w", err)
	}
	// A queue that holds work back does it by suspending the Job, and a suspended Job has no pod: the
	// waiting higher-priority arrival this product exists to notice is invisible in the pod list.
	jobs, err := client.BatchV1().Jobs("").List(ctx, metav1.ListOptions{})
	if err != nil {
		// A cluster with no job API is not a failure: the observer falls back to what it can see.
		return Fold(nodes.Items, pods.Items, opts, now), nil
	}
	classes := map[string]int32{}
	if list, err := client.SchedulingV1().PriorityClasses().List(ctx, metav1.ListOptions{}); err == nil {
		for _, pc := range list.Items {
			classes[pc.Name] = pc.Value
		}
	}

	snap := FoldWithJobs(nodes.Items, pods.Items, jobs.Items, classes, opts, now)
	snap.Summary.Queues = QueueUsage(opts.Dynamic, jobs.Items, now, opts.EventWindow)
	return snap, nil
}

// Fold is the whole observer minus the API call, so it can be tested with plain objects. Without jobs
// it can only see what is already running, which is enough for a plain Kubernetes cluster and blind to
// a queue that holds work back — a suspended Job has no pod at all.
func Fold(nodes []corev1.Node, pods []corev1.Pod, opts Options, now time.Time) Snapshot {
	return FoldWithJobs(nodes, pods, nil, nil, opts, now)
}

// FoldWithJobs folds one read of the cluster, including the jobs the queue has not admitted yet.
func FoldWithJobs(nodes []corev1.Node, pods []corev1.Pod, jobs []batchv1.Job, classes map[string]int32, opts Options, now time.Time) Snapshot {
	stale := int(opts.StaleAfter.Seconds())
	if stale <= 0 {
		stale = 30
	}

	// Per node: what is asked for, and whether anybody declared anything.
	type acc struct {
		req        Resources
		anyPod     bool
		anyRequest bool
	}
	byNode := map[string]*acc{}
	// Pending high-priority pods can land on any node, so the threat is cluster-wide. Counting them
	// per node would pretend the scheduler had already chosen.
	var pendingPriority int32

	units := make([]UnitRecord, 0, 16)
	for i := range pods {
		p := &pods[i]
		prio := int32(0)
		if p.Spec.Priority != nil {
			prio = *p.Spec.Priority
		}
		if p.Status.Phase == corev1.PodPending && prio > 0 {
			pendingPriority++
		}

		if p.Spec.NodeName != "" {
			a := byNode[p.Spec.NodeName]
			if a == nil {
				a = &acc{}
				byNode[p.Spec.NodeName] = a
			}
			a.anyPod = true
			for _, c := range p.Spec.Containers {
				r := c.Resources.Requests
				if _, ok := r[corev1.ResourceCPU]; ok {
					a.anyRequest = true
				}
				a.req.CPUMilli += r.Cpu().MilliValue()
				a.req.MemBytes += r.Memory().Value()
				if g, ok := r["nvidia.com/gpu"]; ok {
					a.req.GPUCount += g.Value()
				}
			}
		}

		if opts.Units == "none" {
			continue
		}
		if opts.Units == "labeled" {
			if _, ok := p.Labels[opts.UnitLabel]; !ok {
				continue
			}
		}
		u := UnitRecord{
			Schema: 1, Cluster: opts.Cluster, UnitID: p.Namespace + "/" + p.Name, At: now.UTC(),
			Node: p.Spec.NodeName, Priority: prio, QoS: string(p.Status.QOSClass),
		}
		if p.DeletionTimestamp != nil {
			t := p.DeletionTimestamp.Time
			u.DeletionTimestamp = &t
		}
		units = append(units, u)
	}
	for i := range units {
		units[i].HigherPriorityWaiting = higherPriorityWaiting(units[i].Priority, jobs, classes, now, opts.EventWindow)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].UnitID < units[j].UnitID })

	// What the queue is holding back. Read from the jobs when the caller could list them, and from the
	// pods otherwise — a plain cluster has no suspended work, so the fallback is the honest answer
	// there and not a guess.
	waiting := int(pendingPriority)
	if jobs != nil {
		waiting = demandCount(jobs, now, opts.EventWindow)
	}
	assignPermits(units, waiting)

	summary := Summary{
		Schema: 1, Cluster: opts.Cluster, At: now.UTC(), StaleAfterSeconds: stale,
		PendingEvent: pendingEvent(jobs, now, opts.EventWindow),
		Arrivals:     arrivals(jobs, now, opts.EventWindow, classes),
		Nodes:        make([]NodeState, 0, len(nodes)),
	}
	for i := range nodes {
		n := &nodes[i]
		a := byNode[n.Name]
		if a == nil {
			a = &acc{}
		}
		ns := NodeState{
			Name:        n.Name,
			Allocatable: resources(n.Status.Allocatable),
			Requested:   a.req,
			Cordoned:    n.Spec.Unschedulable,
			// Only a node with pods and no declared requests is unknown: an empty node really is 0%.
			RequestsUnknown:       a.anyPod && !a.anyRequest,
			PendingHigherPriority: int(pendingPriority),
		}
		for _, t := range n.Spec.Taints {
			ns.Taints = append(ns.Taints, string(t.Key)+":"+string(t.Effect))
		}
		summary.Nodes = append(summary.Nodes, ns)
	}
	sort.Slice(summary.Nodes, func(i, j int) bool { return summary.Nodes[i].Name < summary.Nodes[j].Name })

	summary.Capabilities = map[string]Capability{
		// A cluster that runs a scheduler with priorities can preempt; a pod with a non-zero
		// priority is the evidence that somebody configured one.
		"preemption": {Exists: pendingPriority > 0 || anyPrioritySet(pods), Source: "pods/priority"},
		// kubelet evicts on every Kubernetes node by default.
		"eviction": {Exists: true, Source: "kubelet"},
	}
	return Snapshot{Summary: summary, Units: units}
}

func anyPrioritySet(pods []corev1.Pod) bool {
	for i := range pods {
		if pods[i].Spec.Priority != nil && *pods[i].Spec.Priority != 0 {
			return true
		}
	}
	return false
}

func resources(l corev1.ResourceList) Resources {
	gpu := l["nvidia.com/gpu"]
	return Resources{
		CPUMilli: l.Cpu().MilliValue(),
		MemBytes: l.Memory().Value(),
		GPUCount: gpu.Value(),
	}
}

// unitFileName mirrors the controller's naming: the hash keeps the directory flat and avoids
// encoding a unit id that contains a slash. The id is written inside the file as well, so a collision
// shows up as a mismatch rather than as one job's risk attached to another.
func unitFileName(unitID string) string {
	sum := sha1.Sum([]byte(unitID))
	return hex.EncodeToString(sum[:]) + ".json"
}

// Write puts the snapshot on disk: one summary and one file per unit, each atomically.
func Write(dir string, s Snapshot) error {
	if err := writeJSON(filepath.Join(dir, s.Summary.Cluster, "summary.json"), s.Summary); err != nil {
		return err
	}
	for _, u := range s.Units {
		if err := writeJSON(filepath.Join(dir, u.Cluster, "units", unitFileName(u.UnitID)), u); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Arrival is one job asking for room, or one that has just taken it.
type Arrival struct {
	Name      string    `json:"name"`
	Priority  int32     `json:"priority"`
	CreatedAt time.Time `json:"createdAt"`
}
