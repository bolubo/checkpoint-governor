package policy

import (
	"fmt"
	"testing"
)

// TestTheCatalogueMatchesTheRecord pins the frozen names, tiers, missing policies and order against
// decision 0013. A rename or a reorder is a contract change, so it fails
// here first; the implementation layer builds adapters against these strings and this order.
func TestTheCatalogueMatchesTheRecord(t *testing.T) {
	want := []struct {
		name    string
		tier    Tier
		missing MissingPolicy
	}{
		{"watch.broken", TierUrgent, MissingInert},
		{"risk.preempt", TierUrgent, MissingUrgent},
		{"risk.shutdown", TierUrgent, MissingUrgent},
		{"store.low-space", TierUrgent, MissingUrgent},
		{"host.memory-pressure", TierUrgent, MissingUrgent},
		{"host.gpu-fault", TierUrgent, MissingUrgent},
		{"job.ending", TierUrgent, MissingInert},
		{"risk.eviction-likely", TierUrgent, MissingUrgent},
		{"store.blocked", TierWarn, MissingInert},
		{"store.slow", TierWarn, MissingInert},
		{"host.gpu-hot", TierNote, MissingInert},
		{"host.cpu-throttled", TierNote, MissingInert},
		{"job.step-slow", TierNote, MissingInert},
		{"net.long-outage", TierNote, MissingInert},
	}

	got := Conditions()
	if len(got) != len(want) {
		t.Fatalf("catalogue has %d entries, want %d: %v", len(got), len(want), names(got))
	}
	for i := range want {
		if got[i].Name != want[i].name || got[i].Tier != want[i].tier || got[i].Missing != want[i].missing {
			t.Fatalf("entry %d = %s/%s/%s, want %s/%s/%s", i,
				got[i].Name, got[i].Tier, got[i].Missing,
				want[i].name, want[i].tier, want[i].missing)
		}
		if got[i].Why == "" {
			t.Fatalf("%s has no sentence for a person", got[i].Name)
		}
	}

	urgent, shown := 0, 0
	for _, c := range got {
		if c.Tier == TierUrgent {
			urgent++
		} else {
			shown++
		}
	}
	fmt.Printf("conditions: %d total, %d urgent, %d show-only\n", len(got), urgent, shown)
}

func names(cs []Condition) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}
