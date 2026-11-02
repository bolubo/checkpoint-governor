package metrics

import (
	"strings"
	"testing"
	"time"
)

// The exposition must be readable by a scraper: every sample carries unit and cluster, an unknown
// staleness is +Inf rather than zero, and the counters are one line per kind.
func TestTheExpositionIsValidAndHonest(t *testing.T) {
	got := Render([]Sample{
		{Unit: "job-a", Cluster: "kind-a", Staleness: 812 * time.Second, Covered: true,
			OverBound: 0, Blind: 0, Writes: map[string]int{"window": 12, "final": 1}},
		{Unit: "job-b", Cluster: "kind-b", Covered: false, Writes: map[string]int{}},
	})

	for _, want := range []string{
		"# TYPE checkpoint_staleness_seconds gauge",
		`checkpoint_staleness_seconds{unit="job-a",cluster="kind-a"} 812.000`,
		// No copy: infinite staleness, never a zero that would read as "a fresh checkpoint".
		`checkpoint_staleness_seconds{unit="job-b",cluster="kind-b"} +Inf`,
		`checkpoint_writes_total{unit="job-a",cluster="kind-a",kind="final"} 1`,
		`checkpoint_writes_total{unit="job-a",cluster="kind-a",kind="window"} 12`,
		"checkpoint_units 2",
		"checkpoint_clusters 2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	// An empty cluster is still one dimension, not a missing label, and it is not counted.
	if strings.Contains(got, "checkpoint_clusters 3") {
		t.Fatalf("empty cluster was counted:\n%s", got)
	}
	t.Logf("%s", strings.TrimSpace(got))
}
