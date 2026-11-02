package world

import (
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
)

// suspended reports whether the queue is holding this job back.
//
// A job waiting for quota is suspended: its pods do not exist, so nothing about it appears in the pod
// list. That is why the waiting work has to be read from here — it is the fact this product exists to
// notice, and it is invisible one layer down.
func suspended(j batchv1.Job) bool {
	return j.Spec.Suspend != nil && *j.Spec.Suspend && j.DeletionTimestamp == nil
}

// pendingEvent names the waiting work: the suspended jobs, ordered and hashed.
//
// This is the identity the one-decision-per-event rule needs (0022). Two reads of the same waiting
// work produce the same id — it is still the same thing happening — while a different waiting job
// produces a different one and gets its own decision. It is a fact about the arrival, not a verdict
// about who will be taken.
func pendingEvent(jobs []batchv1.Job, now time.Time, window time.Duration) string {
	names := demandNames(jobs, now, window)
	if len(names) == 0 {
		return ""
	}
	h := fnv.New64a()
	for _, n := range names {
		_, _ = h.Write([]byte(n))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("reclaim:%x", h.Sum64())
}

// suspendedCount is how many workloads the queue is holding back. With one slot per arrival that is
// also the number of units which must die to let them in — the estimate the fanout comparison needs.
func demandCount(jobs []batchv1.Job, now time.Time, window time.Duration) int {
	return len(demandNames(jobs, now, window))
}

// higherPriorityWaiting counts the jobs the queue holds back that outrank this unit.
//
// It is the second term of the measured rule. A unit with nothing above it cannot be the one taken to
// make room, which is why equal priorities do not preempt — measured, not assumed.
func higherPriorityWaiting(mine int32, jobs []batchv1.Job, classes map[string]int32, now time.Time, window time.Duration) int {
	n := 0
	for _, j := range jobs {
		if waitingOrArrived(j, now, window) && jobPriority(j, classes) > mine {
			n++
		}
	}
	return n
}

// jobPriority reads the number the scheduler compares, wherever it actually is.
//
// A job waiting for quota has no pods yet, and its template's `priority` is filled in by the admission
// plugin when the pod is created — so on a suspended job that field is nil. The only place the number
// exists is the PriorityClass the template names, which is why the class table is passed in.
func jobPriority(j batchv1.Job, classes map[string]int32) int32 {
	if j.Spec.Template.Spec.Priority != nil {
		return *j.Spec.Template.Spec.Priority
	}
	if v, ok := classes[j.Spec.Template.Spec.PriorityClassName]; ok {
		return v
	}
	return 0
}

// arrivals publishes the same fact the event id is built from, but unaggregated.
//
// The governor compares this list against the write it last made; the observer cannot, because that
// comparison is memory and the observer keeps none (0022).
func arrivals(jobs []batchv1.Job, now time.Time, window time.Duration, classes map[string]int32) []Arrival {
	out := make([]Arrival, 0, len(jobs))
	for _, j := range jobs {
		if !waitingOrArrived(j, now, window) {
			continue
		}
		out = append(out, Arrival{
			Name:      j.Namespace + "/" + j.Name,
			Priority:  jobPriority(j, classes),
			CreatedAt: j.CreationTimestamp.Time,
		})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	// A cluster with hundreds of pending jobs should not make every summary enormous; the highest
	// priorities are what a unit cares about, and they sort first.
	if len(out) > 200 {
		sort.Slice(out, func(i, k int) bool { return out[i].Priority > out[k].Priority })
		out = out[:200]
	}
	return out
}

func demandNames(jobs []batchv1.Job, now time.Time, window time.Duration) []string {
	var names []string
	for _, j := range jobs {
		if !waitingOrArrived(j, now, window) {
			continue
		}
		names = append(names, j.Namespace+"/"+j.Name)
	}
	sort.Strings(names)
	return names
}
