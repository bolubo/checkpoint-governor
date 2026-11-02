package adapters

import (
	"fmt"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/produce"
)

// evictionLikelyLine is the share of allocatable resources above which a node has no room left. It is
// the fallback shortfall on a cluster with no queue: deployment configuration
// (`conditions.json`), never a runtime knob.
const evictionLikelyLine = 0.90

// evictionLikely is `risk.eviction-likely`: an *inference*, not an announcement, and it reads only
// the world projection — never the cluster itself. The four guardrails (0013, design §5) are all
// visible here: its own name, an answer that names the signals, `unknown` when the world cannot be
// read, and a capability switch to turn it off.
//
// The rule is a conjunction, and it is a conjunction because it was measured (experiments/kueue, 12
// cases): preemption happens when a higher priority is waiting **and** there is a shortfall **and** the
// queue is allowed to take it. The previous rule was an OR of four signals, so it fired whenever
// anything looked tight — including the case where a higher-priority job waits and nothing is taken,
// which is half the grid.
type evictionLikely struct {
	every      time.Duration
	line       float64
	authorized bool
	world      WorldLoc
}

func (evictionLikely) with(c Config) evictionLikely {
	return evictionLikely{
		every:      15 * time.Second,
		line:       c.evictionLikelyLine(),
		authorized: c.evictionAuthorized(),
		world:      c.worldLoc(),
	}
}

func (p evictionLikely) Name() string { return "risk.eviction-likely" }

func (p evictionLikely) Every() time.Duration { return p.every }

func (p evictionLikely) Cost() produce.Cost { return produce.Cheap }

func (p evictionLikely) Read(env produce.Env) produce.Reading {
	if !p.authorized {
		// Switched off, or the operator declared there is no eviction upstream. Absent, not calm: a
		// deployment that turned the inference off has no such condition, and saying "0" would claim
		// it looked and found nothing.
		return produce.Reading{State: produce.Absent, At: env.Now}
	}

	v, err := p.world.project(env.Now)
	if err != nil {
		return produce.Reading{State: produce.Unknown, At: env.Now, Err: err}
	}
	return judgeEviction(v, p.line, p.signals(v), env.Now)
}

// judgeEviction is the shared decision, so the probe and the display cannot drift apart.
func judgeEviction(v UnitView, line float64, fired []string, now time.Time) produce.Reading {
	switch {
	case !v.SummaryPresent || !v.Tracking:
		// No observer, or an observer that reports nodes but not units. Either way this deployment
		// does not have the condition, which is different from failing to read it.
		return produce.Reading{State: produce.Absent, At: now}
	case !v.UnitPresent:
		return produce.Reading{State: produce.Unknown, At: now,
			Err: fmt.Errorf("the observer tracks units but has not placed this one")}
	case v.Stale:
		return produce.Reading{State: produce.Unknown, At: now,
			Err: fmt.Errorf("the world is %s old, past its own limit", v.Age.Round(time.Second))}
	case !v.QueueKnown && !v.RatiosKnown:
		return produce.Reading{State: produce.Unknown, At: now,
			Err: fmt.Errorf("no allocation ratio is readable on %s and the cluster publishes no queue numbers", v.Node)}
	}
	return produce.Reading{State: produce.Fresh, Value: likely(v, line), At: now}
}

// likely is the measured conjunction: somebody higher is waiting, there is a shortfall, and the queue
// may take it. Any one of the three false means nobody dies — which is not the same as "no pressure",
// it is "pressure that will not reach this unit".
func likely(v UnitView, line float64) bool {
	if v.HigherPriorityWaiting <= 0 {
		// Nobody above this unit is waiting for room: it cannot be the one taken to make some.
		return false
	}
	if v.QueueKnown {
		return v.ShortfallMilli > 0 && v.PreemptionAllowed
	}
	// No queue: the node's own headroom is the only shortfall there is, and the line is the operator's
	// own number for "no room left".
	return v.CPUUse >= line || v.MemUse >= line
}

// signals names what fired, which is the guardrail "a 1 must be able to say what it saw".
func (p evictionLikely) signals(v UnitView) []string {
	var out []string
	if v.HigherPriorityWaiting > 0 {
		out = append(out, fmt.Sprintf("%d higher-priority waiting", v.HigherPriorityWaiting))
	}
	if v.QueueKnown {
		if v.ShortfallMilli > 0 {
			out = append(out, fmt.Sprintf("shortfall %dm", v.ShortfallMilli))
		}
		if !v.PreemptionAllowed {
			out = append(out, "queue may not preempt")
		}
		return out
	}
	if v.CPUUse >= p.line {
		out = append(out, fmt.Sprintf("allocatable %d%%", pct(v.CPUUse)))
	}
	if v.MemUse >= p.line {
		out = append(out, fmt.Sprintf("allocatable memory %d%%", pct(v.MemUse)))
	}
	if v.Cordoned {
		out = append(out, "cordoned")
	}
	return out
}

// EvictionSignals is what the display prints behind a 1. It projects the world again, with the same
// rules as the probe: a condition that fires without being able to say which signal fired is exactly
// what 0013 forbids.
func EvictionSignals(worldDir, cluster, unitID string, declared Deployment, line float64, now time.Time) ([]string, error) {
	v, err := Project(worldDir, cluster, unitID, declared, now)
	if err != nil {
		return nil, err
	}
	if !v.SummaryPresent || !v.Tracking || !v.UnitPresent || v.Stale {
		return nil, nil
	}
	return evictionLikely{line: line}.signals(v), nil
}

func pct(f float64) int { return int(f*100 + 0.5) }
