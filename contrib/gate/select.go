package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// needOf is ONE pod's CPU request in milli-cores: the sum of its containers, plus the largest init
// container, which is the rule the scheduler uses. A job that declares nothing needs nothing, and
// the gate then has no arithmetic to do. The arrival's total is needOf × podsOf: a multi-node job
// asks for one pod's worth on each node it lands on, and the pods share the nodes when there are
// fewer nodes than pods.
func needOf(job *batchv1.Job) int64 {
	if job == nil {
		return 0
	}
	need := int64(0)
	for _, c := range job.Spec.Template.Spec.Containers {
		need += c.Resources.Requests.Cpu().MilliValue()
	}
	for _, c := range job.Spec.Template.Spec.InitContainers {
		if v := c.Resources.Requests.Cpu().MilliValue(); v > need {
			need = v
		}
	}
	return need
}

// podsOf is how many pods the arrival has in flight. A Job's parallelism is the count; zero or
// absent means one. Completions index pods one after another, so they do not add concurrent
// requests and do not enter this arithmetic.
func podsOf(job *batchv1.Job) int {
	if job == nil || job.Spec.Parallelism == nil || *job.Spec.Parallelism <= 0 {
		return 1
	}
	return int(*job.Spec.Parallelism)
}

// priorityOf is the arrival's effective priority. A value in the pod spec wins; otherwise the class
// is resolved by the caller from the cluster. Zero means "no priority", and then nothing is below it.
func priorityOf(job *batchv1.Job) int32 {
	if job == nil || job.Spec.Template.Spec.Priority == nil {
		return 0
	}
	return *job.Spec.Template.Spec.Priority
}

// queueRoom is what the queue can still give, with a cohort's lending included.
func queueRoom(q adapters.QueueWire) int64 {
	if q.Cohort != "" {
		return q.CohortFreeMilli
	}
	return q.FreeMilli
}

// admissible is the gate that runs before any victim is chosen (0023): the queue must allow
// preemption, and work past its capacity is never admitted, so it displaces nobody and must not be
// delayed. A false return carries the sentence to print.
func admissible(q adapters.QueueWire, totalNeed int64) (bool, string) {
	if totalNeed <= 0 {
		return false, "the arrival asks for nothing"
	}
	if !q.Allowed {
		return false, fmt.Sprintf("queue %s may not preempt", q.Name)
	}
	if q.CapacityMilli > 0 && totalNeed > q.CapacityMilli {
		return false, fmt.Sprintf("the work needs %dm, past queue %s's capacity of %dm", totalNeed, q.Name, q.CapacityMilli)
	}
	return true, ""
}

// nodePick is one node the arrival's pods may land on, how much room it has, and how many of the
// arrival's pods the plan puts there.
type nodePick struct {
	Name      string
	FreeMilli int64
	Pods      int
}

// pickNodes spreads the arrival's pods over the emptiest readable nodes: at most one node per pod,
// and when there are fewer nodes than pods the pods share them evenly. Cordoned nodes and nodes
// whose requests are unknown are skipped — a node we cannot measure is not a place to look for a
// victim.
func pickNodes(nodes []adapters.NodeState, pods int) []nodePick {
	readable := make([]nodePick, 0, len(nodes))
	for _, n := range nodes {
		if n.RequestsUnknown || n.Cordoned || n.Allocatable.CPUMilli <= 0 {
			continue
		}
		free := n.Allocatable.CPUMilli - n.Requested.CPUMilli
		if free < 0 {
			free = 0
		}
		readable = append(readable, nodePick{Name: n.Name, FreeMilli: free})
	}
	if pods <= 0 || len(readable) == 0 {
		return nil
	}
	// A stable order that two runs agree on: most room first, then the name.
	sort.SliceStable(readable, func(i, j int) bool {
		if readable[i].FreeMilli != readable[j].FreeMilli {
			return readable[i].FreeMilli > readable[j].FreeMilli
		}
		return readable[i].Name < readable[j].Name
	})
	k := pods
	if k > len(readable) {
		k = len(readable)
	}
	return distributePods(readable[:k], pods)
}

// distributePods gives each picked node an even share of the pods, with the remainder going to the
// emptiest first (the slice is already in that order).
func distributePods(picks []nodePick, pods int) []nodePick {
	out := make([]nodePick, len(picks))
	copy(out, picks)
	if len(out) == 0 {
		return nil
	}
	base, extra := pods/len(out), pods%len(out)
	for i := range out {
		out[i].Pods = base
		if i < extra {
			out[i].Pods++
		}
	}
	return out
}

// shortfallPerNode is how much room each node must free before the pods placed there fit. Room freed
// on one node cannot hold a pod that must land on another: this is the whole reason the fanout is
// per node and not one global number.
func shortfallPerNode(picks []nodePick, perPod int64) []int64 {
	out := make([]int64, len(picks))
	for i, p := range picks {
		need := perPod * int64(p.Pods)
		if d := need - p.FreeMilli; d > 0 {
			out[i] = d
		}
	}
	return out
}

// roomPlan is the arithmetic between one arrival and one world: the nodes its pods may use, the
// shortfall each node has, and the shortfall the queue has. It reads no checkpoint and picks no
// victim; it answers "is there a reclaim at all".
type roomPlan struct {
	Picks      []nodePick
	PerPod     int64
	Pods       int
	Total      int64
	QueueShort int64
	NodeShort  []int64
	// Want is the room the victims together must free: every node's gap must be covered, and the
	// queue's gap must be covered somewhere. The queue gap can pull victims from any selected node;
	// a node gap cannot.
	Want int64
}

// planRoom builds the plan. pinned is the operator's node list (empty means auto). A false return
// means the gate must not hold, and why is the sentence to print.
func planRoom(nodes []adapters.NodeState, q adapters.QueueWire, perPod int64, pods int, pinned []string) (roomPlan, bool, string) {
	p := roomPlan{PerPod: perPod, Pods: pods, Total: perPod * int64(pods)}
	if pods <= 0 {
		return p, false, "the arrival has no pods"
	}
	if ok, why := admissible(q, p.Total); !ok {
		return p, false, why
	}

	if len(pinned) > 0 {
		if len(pinned) > pods {
			pinned = pinned[:pods]
		}
		picks := make([]nodePick, 0, len(pinned))
		seen := map[string]bool{}
		for _, name := range pinned {
			if seen[name] {
				continue
			}
			seen[name] = true
			n, ok := findNode(nodes, name)
			if !ok {
				return p, false, fmt.Sprintf("node %q is not in the world", name)
			}
			if n.RequestsUnknown || n.Cordoned || n.Allocatable.CPUMilli <= 0 {
				return p, false, fmt.Sprintf("node %q cannot hold the arrival (cordoned or unreadable)", name)
			}
			free := n.Allocatable.CPUMilli - n.Requested.CPUMilli
			if free < 0 {
				free = 0
			}
			picks = append(picks, nodePick{Name: name, FreeMilli: free})
		}
		p.Picks = distributePods(picks, pods)
	} else {
		p.Picks = pickNodes(nodes, pods)
	}
	if len(p.Picks) == 0 {
		return p, false, "no node is readable: submitted without a hold"
	}

	p.NodeShort = shortfallPerNode(p.Picks, perPod)
	if room := queueRoom(q); p.Total > room {
		p.QueueShort = p.Total - room
	}
	p.Want = p.QueueShort
	var sumNode int64
	for _, s := range p.NodeShort {
		sumNode += s
	}
	if sumNode > p.Want {
		p.Want = sumNode
	}
	if p.Want == 0 {
		return p, false, "the queue and the nodes already have room"
	}
	return p, true, ""
}

// findNode looks a node up by name.
func findNode(nodes []adapters.NodeState, name string) (adapters.NodeState, bool) {
	for _, n := range nodes {
		if n.Name == name {
			return n, true
		}
	}
	return adapters.NodeState{}, false
}

// pickVictims chooses, per node, the units that should write. Node shortfalls are satisfied node by
// node — room freed on worker-1 cannot hold a pod that must land on worker-2 — and the queue's
// remaining gap is topped up from any selected node. All-or-nothing: if a node that must free room
// has no governed unit below the arrival, or the victims cannot free what the plan asks, the gate
// does not hold at all.
func pickVictims(p roomPlan, units []adapters.UnitRecord, sizes map[string]int64, dirs map[string]string, priority int32) ([]permit.Unit, string) {
	var out []permit.Unit
	chosen := map[string]bool{}
	for i, pick := range p.Picks {
		if p.NodeShort[i] <= 0 {
			continue
		}
		cands := candidates(units, sizes, dirs, pick.Name, priority)
		v := permit.Victims(cands, permit.CauseReclaim, int(p.NodeShort[i]))
		if len(v) == 0 {
			return nil, fmt.Sprintf("no governed unit below this priority on %s can free %dm", pick.Name, p.NodeShort[i])
		}
		for _, u := range v {
			if !chosen[u.ID] {
				chosen[u.ID] = true
				out = append(out, u)
			}
		}
	}

	freed := int64(0)
	for _, u := range out {
		freed += int64(u.Size)
	}
	if freed < p.Want {
		var rest []permit.Unit
		for _, pick := range p.Picks {
			for _, u := range candidates(units, sizes, dirs, pick.Name, priority) {
				if !chosen[u.ID] {
					rest = append(rest, u)
				}
			}
		}
		for _, u := range permit.Victims(rest, permit.CauseReclaim, int(p.Want-freed)) {
			if chosen[u.ID] {
				continue
			}
			chosen[u.ID] = true
			out = append(out, u)
			freed += int64(u.Size)
		}
	}
	if freed < p.Want {
		return nil, fmt.Sprintf("the victims can free %dm, but the plan asks for %dm", freed, p.Want)
	}
	if len(out) == 0 {
		return nil, "no unit can be held"
	}
	return out, ""
}

// candidates are the workloads a reclaim may take on one node: strictly lower priority than the
// arrival, placed on the chosen node, governed by this deployment (at least one of their pods has a
// checkpoint directory under --ckpt-root), and with a size the gate could read.
//
// The directory clause is what keeps the cluster's own pods out. An observer that records every pod
// also writes the controller-manager, and selecting it would be asking a system pod for a checkpoint
// it does not have; worse, it can outrank a real victim on the priority sort and hide it.
//
// A multi-pod workload is one candidate with the footprint of its pods **on this node**: a pod on
// another node does not free room here, but its shard still belongs to the copy the workload seals.
func candidates(units []adapters.UnitRecord, sizes map[string]int64, dirs map[string]string, node string, arrivalPriority int32) []permit.Unit {
	type workload struct {
		id       string
		priority int32
		size     int64
		governed bool
	}
	byKey := map[string]*workload{}
	var order []string
	for _, u := range units {
		if u.Node != node || u.Priority >= arrivalPriority {
			continue
		}
		key := u.Workload
		if key == "" {
			key = u.UnitID
		}
		w := byKey[key]
		if w == nil {
			w = &workload{id: key, priority: u.Priority}
			byKey[key] = w
			order = append(order, key)
		}
		if size, ok := sizes[u.UnitID]; ok && size > 0 {
			w.size += size
		}
		if _, governed := dirs[u.UnitID]; governed {
			w.governed = true
		}
	}

	out := make([]permit.Unit, 0, len(order))
	for _, key := range order {
		w := byKey[key]
		if !w.governed || w.size <= 0 {
			continue
		}
		out = append(out, permit.Unit{ID: w.id, Priority: int(w.priority), Size: int(w.size)})
	}
	return out
}

// workloadVictim is one workload the gate holds. A preemption stops every pod of a Job at once, so
// holding one pod of a two-pod workload would ask for the same write twice and release the arrival
// after the first one. `ID` is the workload's id when it has more than one pod, and the pod's own
// unit id when it has one — which keeps the single-pod output exactly what it always was (0027).
type workloadVictim struct {
	ID       string
	Pods     []string
	Nodes    []string
	Size     int64
	Priority int32
}

// groupByWorkload turns the chosen pods into the workloads that must write. Every pod of a chosen
// workload joins the hold, not only the pod the arithmetic picked: the victim loses all of them, so
// the room they free is the workload's whole footprint.
func groupByWorkload(chosen []permit.Unit, units []adapters.UnitRecord, sizes map[string]int64) []workloadVictim {
	byID := make(map[string]adapters.UnitRecord, len(units))
	for _, u := range units {
		byID[u.UnitID] = u
	}
	keyOf := func(id string) string {
		if u, ok := byID[id]; ok && u.Workload != "" {
			return u.Workload
		}
		return id
	}

	var out []workloadVictim
	seen := map[string]bool{}
	for _, c := range chosen {
		key := keyOf(c.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		w := workloadVictim{ID: key, Priority: int32(c.Priority)}
		nodes := map[string]bool{}
		for _, u := range units {
			if keyOf(u.UnitID) != key {
				continue
			}
			w.Pods = append(w.Pods, u.UnitID)
			if u.Node != "" && !nodes[u.Node] {
				nodes[u.Node] = true
				w.Nodes = append(w.Nodes, u.Node)
			}
			if s, ok := sizes[u.UnitID]; ok && s > 0 {
				w.Size += s
			}
		}
		if len(w.Pods) == 1 {
			w.ID = w.Pods[0]
		}
		sort.Strings(w.Nodes)
		out = append(out, w)
	}
	return out
}

// victimsLine renders one workload. The single-pod forms are the sentences earlier releases printed,
// down to the comma, because a user's log grep must not change under them.
func victimsLine(w workloadVictim, oneNode bool) string {
	if len(w.Pods) == 1 {
		if oneNode {
			return fmt.Sprintf("%s (priority %d, size %dm)", w.ID, w.Priority, w.Size)
		}
		return fmt.Sprintf("%s (%s, priority %d, size %dm)", w.ID, w.Nodes[0], w.Priority, w.Size)
	}
	return fmt.Sprintf("%s (%s, priority %d, size %dm)", w.ID, joinStrings(w.Nodes, ","), w.Priority, w.Size)
}

// perNodeCheckpoint names the first pod whose checkpoint root is a node-local hostPath mount. A
// multi-pod workload on such a volume does not share one root: rank 1 writes to a directory rank 0
// cannot see, so no seal will ever make the workload whole and the gate must refuse to wait (0027).
func perNodeCheckpoint(pods []corev1.Pod, unitIDs []string, ckptRoot string) (string, string) {
	root := filepath.Clean(ckptRoot)
	for _, uid := range unitIDs {
		ns, name, ok := strings.Cut(uid, "/")
		if !ok {
			continue
		}
		for i := range pods {
			p := &pods[i]
			if p.Namespace != ns || p.Name != name {
				continue
			}
			for _, v := range p.Spec.Volumes {
				if v.HostPath == nil {
					continue
				}
				for _, c := range p.Spec.Containers {
					for _, m := range c.VolumeMounts {
						if m.Name != v.Name {
							continue
						}
						mp := filepath.Clean(m.MountPath)
						if root == mp || strings.HasPrefix(root, mp+"/") {
							return uid, v.HostPath.Path
						}
					}
				}
			}
		}
	}
	return "", ""
}
func pickNode(nodes []adapters.NodeState) (string, int64) {
	best, bestFree := "", int64(0)
	for _, n := range nodes {
		if n.RequestsUnknown || n.Allocatable.CPUMilli <= 0 {
			continue
		}
		free := n.Allocatable.CPUMilli - n.Requested.CPUMilli
		if free < 0 {
			free = 0
		}
		if best == "" || free > bestFree {
			best, bestFree = n.Name, free
		}
	}
	return best, bestFree
}

// newestManifest is the newest complete checkpoint under one directory, and its step. Sealing is a
// manifest that appeared after the hold began; the step is for the line a person reads.
func newestManifest(dir string, since time.Time) (time.Time, int64, bool) {
	entries, err := readDir(dir)
	if err != nil {
		return time.Time{}, 0, false
	}
	var newest time.Time
	var step int64
	for _, e := range entries {
		if !e.IsDir() || !dirPrefix(e.Name()) {
			continue
		}
		info, err := statManifest(dir, e.Name())
		if err != nil || info.ModTime().Before(since) {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
			step = stepOfCheckpoint(dir, e.Name())
		}
	}
	if newest.IsZero() {
		return time.Time{}, 0, false
	}
	return newest, step, true
}

// cpuSizes reads each unit's CPU request from the cluster once. The world deliberately carries no
// sizes — a unit record is "who could die", and adding a size would make it a different fact — so the
// gate asks the pods it is about to hold the arrival against.
func cpuSizes(pods []corev1.Pod) map[string]int64 {
	out := map[string]int64{}
	for i := range pods {
		p := &pods[i]
		id := p.Namespace + "/" + p.Name
		size := int64(0)
		for _, c := range p.Spec.Containers {
			size += c.Resources.Requests.Cpu().MilliValue()
		}
		for _, c := range p.Spec.InitContainers {
			if v := c.Resources.Requests.Cpu().MilliValue(); v > size {
				size = v
			}
		}
		out[id] = size
	}
	return out
}
