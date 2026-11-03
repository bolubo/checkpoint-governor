package main

import (
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// needOf is the arrival's CPU request in milli-cores: the sum of its containers, plus the largest
// init container, which is the rule the scheduler uses. A job that declares nothing needs nothing,
// and the gate then has no arithmetic to do.
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

// priorityOf is the arrival's effective priority. A value in the pod spec wins; otherwise the class
// is resolved by the caller from the cluster. Zero means "no priority", and then nothing is below it.
func priorityOf(job *batchv1.Job) int32 {
	if job == nil || job.Spec.Template.Spec.Priority == nil {
		return 0
	}
	return *job.Spec.Template.Spec.Priority
}

// shortfallFor is how much room the arrival must free before it can be admitted: the larger of what
// the queue cannot give and what the node cannot hold. The capacity check is the measured rule's
// (0023): work past a queue's capacity is never admitted, so it displaces nobody and must not be
// delayed. Cohorts lend, so a member's room is the group's.
func shortfallFor(q adapters.QueueWire, nodeFreeMilli, needMilli int64) (int64, bool) {
	if needMilli <= 0 || !q.Allowed {
		return 0, false
	}
	if q.CapacityMilli > 0 && needMilli > q.CapacityMilli {
		return 0, false
	}
	room := q.FreeMilli
	if q.Cohort != "" {
		room = q.CohortFreeMilli
	}
	if nodeFreeMilli < room {
		room = nodeFreeMilli
	}
	if d := needMilli - room; d > 0 {
		return d, true
	}
	return 0, false
}

// candidates are the units a reclaim may take: strictly lower priority than the arrival, placed on
// the chosen node, governed by this deployment (its checkpoint directory is under --ckpt-root), and
// with a size the gate could read.
//
// The directory clause is what keeps the cluster's own pods out. An observer that records every pod
// also writes the controller-manager, and selecting it would be asking a system pod for a checkpoint
// it does not have; worse, it can outrank a real victim on the priority sort and hide it.
func candidates(units []adapters.UnitRecord, sizes map[string]int64, dirs map[string]string, node string, arrivalPriority int32) []permit.Unit {
	out := make([]permit.Unit, 0, len(units))
	for _, u := range units {
		if u.Node != node || u.Priority >= arrivalPriority {
			continue
		}
		if _, governed := dirs[u.UnitID]; !governed {
			continue
		}
		size, ok := sizes[u.UnitID]
		if !ok || size <= 0 {
			continue
		}
		out = append(out, permit.Unit{ID: u.UnitID, Priority: int(u.Priority), Size: int(size)})
	}
	return out
}

// pickNode chooses the node with the most CPU room to host the arrival. It is a heuristic, not the
// scheduler: the gate needs somewhere to look for a victim, and the emptiest node is where a job of
// this size is most likely to land. --node pins it when the operator knows better.
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
