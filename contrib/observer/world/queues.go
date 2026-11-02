package world

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// QueueState is what one queue can still admit, and whether it is allowed to take it from somebody.
//
// The measured rule (experiments/kueue) is `shortfall > 0 AND higher priority waiting AND preemption
// allowed`. `free` and `demand` are the first term; `allowed` is the third. Reading them from the queue
// is reading a fact the cluster publishes about itself, not a verdict about who will be taken.
type QueueState struct {
	Name string `json:"name"`
	// FreeMilli is what the queue can still admit, in milli-cores.
	FreeMilli int64 `json:"freeMilli"`
	// DemandMilli is what the work it is holding back is asking for.
	DemandMilli int64 `json:"demandMilli"`
	// Allowed is false when the queue may not preempt within itself: the arrival waits instead.
	Allowed bool `json:"allowed"`
	// Cohort is the group this queue belongs to, empty when it stands alone.
	Cohort string `json:"cohort,omitempty"`
	// CohortFreeMilli is what the whole cohort can still admit. Members lend each other their unused
	// quota, so this — not FreeMilli — is the number a shortfall has to be measured against. Measured:
	// with only the queue's own headroom, a cohort case predicted 3 and 5 preemptions where Kueue took
	// none (experiments/kueue, rows `cohort`).
	CohortFreeMilli int64 `json:"cohortFreeMilli,omitempty"`
}

// ShortfallMilli is how much more the waiting work needs than can be given, counting what the queue's
// cohort would lend it. The cohort number is what the measurement asked for: without it a lending queue
// reports a shortfall that nobody has to die for.
func (q QueueState) ShortfallMilli() int64 {
	room := q.FreeMilli
	if q.Cohort != "" {
		room = q.CohortFreeMilli
	}
	if d := q.DemandMilli - room; d > 0 {
		return d
	}
	return 0
}

var clusterQueueGVR = schema.GroupVersionResource{
	Group: "kueue.x-k8s.io", Version: "v1beta1", Resource: "clusterqueues",
}

// QueueUsage reads every ClusterQueue's leftover quota and its preemption policy, and pairs it with
// the demand of the jobs that are waiting.
//
// A cluster without Kueue returns nothing rather than an error: the product serves clusters that have
// no queue at all, and "no queue" is a fact the caller already handles.
func QueueUsage(dyn dynamic.Interface, jobs []batchv1.Job, now time.Time) []QueueState {
	if dyn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	list, err := dyn.Resource(clusterQueueGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}

	demand := demandByQueue(jobs)
	out := make([]QueueState, 0, len(list.Items))
	for i := range list.Items {
		cq := &list.Items[i]
		q := QueueState{
			Name:    cq.GetName(),
			Allowed: preemptionAllowed(cq),
		}
		q.FreeMilli = nominalMilli(cq) - usedMilli(cq)
		if q.FreeMilli < 0 {
			q.FreeMilli = 0
		}
		q.DemandMilli = demand[cq.GetName()]
		q.Cohort = cohortOf(cq)
		out = append(out, q)
	}

	// A cohort lends its members' unused quota to each other, so the room available to one member is the
	// whole group's. Summed rather than bounded by each member's borrowingLimit: the limit caps what a
	// member may take, and ignoring it over-states the room, which is the safe direction for a rule whose
	// job is to stop warning about preemptions that will not happen.
	byCohort := map[string]int64{}
	for _, q := range out {
		if q.Cohort != "" {
			byCohort[q.Cohort] += q.FreeMilli
		}
	}
	for i := range out {
		if out[i].Cohort != "" {
			out[i].CohortFreeMilli = byCohort[out[i].Cohort]
		}
	}
	return out
}

func cohortOf(cq *unstructured.Unstructured) string {
	name, _, _ := unstructured.NestedString(cq.Object, "spec", "cohort")
	return name
}

// demandByQueue sums the requests of the jobs the queues are holding back, keyed by the queue the job
// asked for. Kueue names the LocalQueue on the job, and a LocalQueue points at a ClusterQueue; in the
// common single-queue deployment the two share a name, and where they do not the demand for a queue
// with no matching name is simply not attributed (the shortfall then reads low, never high).
func demandByQueue(jobs []batchv1.Job) map[string]int64 {
	out := map[string]int64{}
	for _, j := range jobs {
		if !suspended(j) {
			continue
		}
		name := j.Labels["kueue.x-k8s.io/queue-name"]
		if name == "" {
			continue
		}
		for _, c := range j.Spec.Template.Spec.Containers {
			out[name] += c.Resources.Requests.Cpu().MilliValue()
		}
	}
	return out
}

func nominalMilli(cq *unstructured.Unstructured) int64 {
	groups, _, _ := unstructured.NestedSlice(cq.Object, "spec", "resourceGroups")
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		flavors, _, _ := unstructured.NestedSlice(gm, "flavors")
		for _, f := range flavors {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			res, _, _ := unstructured.NestedSlice(fm, "resources")
			for _, r := range res {
				rm, ok := r.(map[string]any)
				if !ok {
					continue
				}
				if name, _, _ := unstructured.NestedString(rm, "name"); name != "cpu" {
					continue
				}
				if v, found, _ := unstructured.NestedString(rm, "nominalQuota"); found {
					return parseMilli(v)
				}
			}
		}
	}
	return 0
}

func usedMilli(cq *unstructured.Unstructured) int64 {
	var total int64
	flavors, _, _ := unstructured.NestedSlice(cq.Object, "status", "flavorsUsage")
	for _, f := range flavors {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		res, _, _ := unstructured.NestedSlice(fm, "resources")
		for _, r := range res {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			if name, _, _ := unstructured.NestedString(rm, "name"); name != "cpu" {
				continue
			}
			if v, found, _ := unstructured.NestedString(rm, "total"); found {
				total += parseMilli(v)
			}
		}
	}
	return total
}

// preemptionAllowed is false only when the queue says Never. An absent policy means the default, which
// allows it.
func preemptionAllowed(cq *unstructured.Unstructured) bool {
	policy, found, _ := unstructured.NestedString(cq.Object, "spec", "preemption", "withinClusterQueue")
	if !found {
		return true
	}
	return policy != "Never"
}

// parseMilli reads a Kubernetes quantity that may be a plain number or a suffixed string. The values
// here are quotas, so a suffix beyond milli is not expected; anything unreadable counts as zero, which
// makes the shortfall read high rather than low — the failure leans toward writing.
func parseMilli(s string) int64 {
	var v int64
	var frac int64
	var div int64 = 1
	seenDot := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			if seenDot {
				frac = frac*10 + int64(r-'0')
				div *= 10
			} else {
				v = v*10 + int64(r-'0')
			}
		case r == '.':
			seenDot = true
		case r == 'm':
			// Already milli-units: `2500m` is 2500, not 2,500,000. Multiplying here is how a queue with
			// 1.5 cores free reported zero, and how every shortfall came out equal to the arrival's size.
			return v + frac/div
		default:
			return v * 1000
		}
	}
	return v*1000 + frac*1000/div
}

func queueNames(qs []QueueState) string {
	out := ""
	for _, q := range qs {
		out += fmt.Sprintf("%s(free=%dm,demand=%dm,preempt=%v) ", q.Name, q.FreeMilli, q.DemandMilli, q.Allowed)
	}
	return out
}
