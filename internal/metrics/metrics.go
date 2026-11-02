// Package metrics renders what a governor knows as Prometheus text.
//
// It is a pure function of samples: nothing here reads a clock, a directory or a network. The
// caller decides what a unit's staleness is; this package only refuses to invent one. That keeps the
// exporter honest — a metric that is unknown is +Inf on a covered gauge, never a zero that looks like
// a healthy checkpoint.
package metrics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sample is one unit's numbers at the moment the caller measured them.
type Sample struct {
	Unit    string
	Cluster string
	// Staleness is the age of the newest complete copy. Covered is false when there is no copy at
	// all: the unit then has no finite staleness, and the gauge says +Inf rather than 0.
	Staleness time.Duration
	Covered   bool
	// OverBound is how long the newest copy was older than the bound the run certified against.
	OverBound time.Duration
	// Blind is how many urgent conditions were unreadable in the last published decision.
	Blind int
	// Writes counts seals by kind ("window", "exception", "final", "repair").
	Writes map[string]int
}

// The metric names are a promise to every scraper and dashboard built on them: a rename is a
// compatibility change, not a cleanup. They are constants, and Names lists them in the documented
// order so a test can pin the set the way the condition catalogue is pinned.
const (
	Staleness   = "checkpoint_staleness_seconds"
	OverBound   = "checkpoint_over_bound_seconds"
	Blind       = "checkpoint_blind_conditions"
	WritesTotal = "checkpoint_writes_total"
	Units       = "checkpoint_units"
	Clusters    = "checkpoint_clusters"
)

// Names is the frozen list of metric names.
func Names() []string {
	return []string{Staleness, OverBound, Blind, WritesTotal, Units, Clusters}
}

// Render writes the exposition format for the samples, plus unit and cluster counts.
func Render(samples []Sample) string {
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Unit != samples[j].Unit {
			return samples[i].Unit < samples[j].Unit
		}
		return samples[i].Cluster < samples[j].Cluster
	})

	var b strings.Builder
	head := func(name, kind, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	}

	head(Staleness, "gauge",
		"Age of the newest complete checkpoint; +Inf when the run has none.")
	for _, s := range samples {
		value := "+Inf"
		if s.Covered {
			value = seconds(s.Staleness)
		}
		line(&b, Staleness, s, value)
	}

	head(OverBound, "gauge",
		"Seconds the newest checkpoint was older than the certified bound.")
	for _, s := range samples {
		line(&b, OverBound, s, seconds(s.OverBound))
	}

	head(Blind, "gauge",
		"Urgent conditions that were unreadable in the last published decision.")
	for _, s := range samples {
		line(&b, Blind, s, strconv.Itoa(s.Blind))
	}

	head(WritesTotal, "counter",
		"Completed writes by kind (window, exception, final, repair).")
	for _, s := range samples {
		kinds := make([]string, 0, len(s.Writes))
		for k := range s.Writes {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			fmt.Fprintf(&b, WritesTotal+"{unit=%q,cluster=%q,kind=%q} %d\n",
				s.Unit, s.Cluster, k, s.Writes[k])
		}
	}

	clusters := map[string]bool{}
	for _, s := range samples {
		if s.Cluster != "" {
			clusters[s.Cluster] = true
		}
	}
	head(Units, "gauge", "Checkpoint directories this governor holds.")
	fmt.Fprintf(&b, Units+" %d\n", len(samples))
	head(Clusters, "gauge", "Distinct clusters among the held units.")
	fmt.Fprintf(&b, Clusters+" %d\n", len(clusters))
	return b.String()
}

func line(b *strings.Builder, name string, s Sample, value string) {
	fmt.Fprintf(b, "%s{unit=%q,cluster=%q} %s\n", name, s.Unit, s.Cluster, value)
}

func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}
