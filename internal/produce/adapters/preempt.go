package adapters

import (
	"fmt"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/produce"
)

// preemption is `risk.preempt`: the platform has decided to take this unit, or is about to.
//
// It is an **announcement**, not an inference, and the difference is the whole point of the pair:
// this condition only ever fires on a fact the platform wrote down (`deletionTimestamp`, `evicted`),
// while `risk.eviction-likely` guesses from pressure. A guess must never be able to rename a
// decision an announcement already made, which is why this one sits earlier in the catalogue.
type preemption struct {
	every      time.Duration
	authorized bool
	// declared is not the same as authorized. Undeclared means assumed on (a deployment with
	// priorities almost always has something enforcing them), and an assumption must never turn a
	// quiet world into a blind one: only an operator who wrote the declaration down gets told that
	// the channel they asked for is silent.
	declared bool
	world    WorldLoc
}

func (preemption) with(c Config) preemption {
	cap := c.Capability(CapPreemption)
	return preemption{
		every:      5 * time.Second,
		authorized: cap.Exists,
		declared:   cap.Declared,
		world:      c.worldLoc(),
	}
}

func (p preemption) Name() string { return "risk.preempt" }

func (p preemption) Every() time.Duration { return p.every }

func (p preemption) Cost() produce.Cost { return produce.Cheap }

func (p preemption) Read(env produce.Env) produce.Reading {
	if !p.authorized {
		// The operator declared this deployment has no preemption upstream. Absent, not calm.
		return produce.Reading{State: produce.Absent, At: env.Now}
	}

	v, err := p.world.project(env.Now)
	if err != nil {
		return produce.Reading{State: produce.Unknown, At: env.Now, Err: err}
	}
	return judgePreemption(v, env.Now, p.declared)
}

// judgePreemption is the shared decision for the probe and the display.
func judgePreemption(v UnitView, now time.Time, declared bool) produce.Reading {
	switch {
	case !v.SummaryPresent:
		// Two different situations, and telling them apart is the whole point of the declaration:
		//
		//   - nobody declared preemption: there is no platform announcement to miss, so absent is
		//     the truth, and a laptop deployment is not nagged about an observer it does not need.
		//   - this deployment *did* declare preemption upstream: the announcement channel exists and
		//     is silent, which is an observer that has not written yet or has stopped. Reporting
		//     absent there is claiming calm about a platform we cannot see.
		if declared {
			return produce.Reading{State: produce.Unknown, At: now,
				Err: fmt.Errorf("preemption is declared for this deployment, and the observer has written nothing")}
		}
		return produce.Reading{State: produce.Absent, At: now}
	case v.Announced:
		// An announcement, from either writer: the platform's deletion timestamp or eviction event, or
		// the gate's held reclaim (decision 0030). Both are a decision already made, and the gate writes
		// it before the platform acts instead of after. It is read before "does this observer track
		// units" on purpose: a gate hold is about this unit by name, and losing that because the
		// observer publishes no unit column would be blindness about a fact we hold.
		return produce.Reading{State: produce.Fresh, Value: true, At: now}
	case !v.Tracking:
		// The observer reports nodes but not units, so no announcement can reach us. Same split: a
		// declared channel that tracks nothing is blind, not calm.
		if declared {
			return produce.Reading{State: produce.Unknown, At: now,
				Err: fmt.Errorf("preemption is declared, and the observer reports nodes but not units")}
		}
		return produce.Reading{State: produce.Absent, At: now}
	case !v.UnitPresent:
		return produce.Reading{State: produce.Unknown, At: now,
			Err: fmt.Errorf("the observer tracks units but has not placed this one")}
	case v.Stale:
		return produce.Reading{State: produce.Unknown, At: now,
			Err: fmt.Errorf("the world is %s old, past its own limit", v.Age.Round(time.Second))}
	}
	return produce.Reading{State: produce.Fresh, Value: false, At: now}
}
