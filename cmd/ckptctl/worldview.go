package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// viewWorld reads this unit's slice of the world for the display. It returns ok=false when there is
// no observer, because a deployment without one has nothing to show and nothing to apologise for.
func viewWorld(dir string, now time.Time) (adapters.UnitView, adapters.Config, bool) {
	cfg, _, err := adapters.LoadConfig(dir)
	if err != nil {
		return adapters.UnitView{}, cfg, false
	}
	dep, err := adapters.LoadDeployment(dir)
	if err != nil {
		return adapters.UnitView{}, cfg, false
	}
	cfg.Deployment = dep
	cfg = cfg.WithWorld(dir)
	v, err := adapters.Project(cfg.WorldDir, cfg.Cluster, cfg.UnitID, dep, now)
	if err != nil || !v.SummaryPresent {
		return adapters.UnitView{}, cfg, false
	}
	return v, cfg, true
}

// worldSummaryLine is the row-level view: one line, decisions only.
func worldSummaryLine(out io.Writer, dir string, now time.Time) {
	v, _, ok := viewWorld(dir, now)
	if !ok {
		return
	}
	fmt.Fprintf(out, "world      %s\n", v.Line())
}

// worldBlock is the expansion: every number, its age, and which signals fired. This is where the
// numbers come back after the boolean border, and they come with their provenance attached.
func worldBlock(out io.Writer, dir string, now time.Time) {
	v, cfg, ok := viewWorld(dir, now)
	if !ok {
		return
	}
	age := v.Age.Round(time.Second)
	if v.Node == "" {
		fmt.Fprintf(out, "world      cluster %s · %s old · this unit is not placed\n", v.Cluster, age)
		return
	}
	fmt.Fprintf(out, "world      node %s/%s %d%% cpu (%s old)\n", v.Cluster, v.Node, pct(v.CPUUse), age)
	if v.RatiosKnown {
		fmt.Fprintf(out, "           %d%% mem · %d%% gpu · %d higher-priority pending\n",
			pct(v.MemUse), pct(v.GPUUse), v.PendingHigherPriority)
	} else {
		fmt.Fprintln(out, "           the allocation ratios cannot be read (no requests declared, or no limits)")
	}
	if v.Cordoned {
		fmt.Fprintln(out, "           the node is cordoned")
	}
	if v.Stale {
		fmt.Fprintln(out, "           STALE: past the limit the observer declared — decisions cannot use these numbers")
	}
	if v.Reclaim != nil {
		// The gate's record is the announcement with lead time, and it says who it is holding: a
		// person reading `explain` must be able to see that a write is being asked for now.
		fmt.Fprintf(out, "basis      announcement: %s\n", v.Reclaim.Line())
	} else if v.Announced {
		fmt.Fprintln(out, "basis      announcement: the platform has already decided (deletion timestamp or eviction event)")
	} else if fired, _ := adapters.EvictionSignals(cfg.WorldDir, cfg.Cluster, cfg.UnitID, cfg.Deployment, cfg.EvictionLineOrDefault(), now); len(fired) > 0 {
		fmt.Fprintf(out, "basis      inference: %s (this is a guess, and a capability can turn it off)\n",
			strings.Join(fired, ", "))
	}
}

func pct(f float64) int { return int(f*100 + 0.5) }
