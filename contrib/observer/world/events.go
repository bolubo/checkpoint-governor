package world

import (
	"fmt"
	"hash/fnv"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// pendingEvent names the waiting work: the unscheduled pods, ordered and hashed.
//
// This is the identity the one-decision-per-event rule needs (0022). Two reads of the same waiting
// workload produce the same id — it is still the same thing happening — while a different waiting
// workload produces a different one and gets its own decision. A hash rather than the names because a
// one-line id is what a person can put in a receipt and compare.
func pendingEvent(pods []corev1.Pod) string {
	var names []string
	for _, p := range pods {
		if p.Spec.NodeName != "" || p.DeletionTimestamp != nil {
			continue
		}
		names = append(names, p.Namespace+"/"+p.Name)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)

	h := fnv.New64a()
	for _, n := range names {
		_, _ = h.Write([]byte(n))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("reclaim:%x", h.Sum64())
}
