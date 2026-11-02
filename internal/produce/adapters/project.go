package adapters

import (
	"fmt"
	"os"
	"time"
)

// UnitView is one unit's slice of the world, and the only thing the condition layer may look at.
//
// It is a **pure function** of three inputs — one summary, one unit record, the clock — so a test
// can hand it a fixture and nothing else, and a controller's cadence is the read's cadence with no
// cache in between. Everything the conditions need is flattened here; everything they must not see
// (hostnames, node objects, clusters) stays outside or is reduced to a number.
type UnitView struct {
	Cluster string
	UnitID  string

	// SummaryPresent is false when no observer has ever written for this cluster. That is a
	// deployment without visibility, not a fault: conditions built on it are absent, not unknown.
	SummaryPresent bool
	// UnitPresent is false when the observer writes summaries but has no record for this unit. The
	// observer knows where units run; if it has not said, we do not know where this one runs.
	UnitPresent bool
	// Tracking is false when this cluster's observer writes no unit records at all. That is a
	// deployment that reports nodes but not units — a different fact from "it lost mine", and the
	// difference decides between absent and unknown.
	Tracking bool

	// Freshness of the summary. Stale means the numbers below may not be believed.
	At    time.Time
	Age   time.Duration
	Stale bool

	// Node facts. RatiosKnown is false when the allocatable numbers are missing or zero, which is
	// exactly the case where a calm answer would be a lie.
	Node                  string
	CPUUse                float64
	MemUse                float64
	GPUUse                float64
	RatiosKnown           bool
	Cordoned              bool
	PendingHigherPriority int
	// HigherPriorityWaiting is how many held-back jobs outrank this unit — the term the measured rule
	// uses, because the node-level count cannot answer "could I be the one taken".
	HigherPriorityWaiting int
	// The queue's numbers, when the cluster publishes them. QueueKnown is false when it does not, and a
	// rule that needs them must then say unknown rather than guess.
	ShortfallMilli    int64
	PreemptionAllowed bool
	QueueKnown        bool

	// Unit facts.
	Priority  int32
	QoS       string
	Announced bool // the platform has decided: deletionTimestamp or evicted

	// Capabilities, resolved: the operator's declaration first, then the summary, then assumed on.
	Capabilities map[string]Capability
}

// WorldLoc is where a controller sits in the world: which directory to read, which cluster, which
// unit, and what the operator declared. It is resolved once at startup so a probe never has to guess
// its own identity in the middle of a decision.
type WorldLoc struct {
	Dir      string
	Cluster  string
	UnitID   string
	Declared Deployment
}

func (w WorldLoc) project(now time.Time) (UnitView, error) {
	return Project(w.Dir, w.Cluster, w.UnitID, w.Declared, now)
}

// Project builds the view. A missing world is not an error; a corrupt one is, because the caller has
// to be able to tell "nothing to see" from "the thing I was told to look at is broken".
func Project(worldDir, cluster, unitID string, declared Deployment, now time.Time) (UnitView, error) {
	v := UnitView{Cluster: cluster, UnitID: unitID}
	if worldDir == "" {
		return v, nil
	}

	s, err := ReadSummary(worldDir, cluster)
	if err != nil {
		if os.IsNotExist(err) {
			return v, nil
		}
		return v, err
	}

	v.SummaryPresent = true
	v.At = s.At
	v.Age = s.Age(now)
	v.Stale = s.Stale(now)
	v.Tracking = UnitTracking(worldDir, cluster)
	v.Capabilities = resolveCapabilities(declared, s)

	u, err := ReadUnit(worldDir, cluster, unitID)
	switch {
	case err == nil:
		v.UnitPresent = true
		v.Node = u.Node
		v.Priority = u.Priority
		v.QoS = u.QoS
		v.Announced = u.Evicted || (u.DeletionTimestamp != nil && !u.DeletionTimestamp.IsZero())
		v.HigherPriorityWaiting = u.HigherPriorityWaiting
	case os.IsNotExist(err):
		// Not an error: this deployment may not track units at all.
	default:
		return v, err
	}

	if v.Node == "" {
		return v, nil
	}
	n, ok := s.Node(v.Node)
	if !ok {
		return v, nil
	}
	v.Cordoned = n.Cordoned
	v.PendingHigherPriority = n.PendingHigherPriority
	if v.HigherPriorityWaiting == 0 {
		// No queue holds work back here, so the node's pending count is the same fact measured one
		// layer down. Without this, a cluster with no Kueue could never report the condition.
		v.HigherPriorityWaiting = n.PendingHigherPriority
	}
	if n.Allocatable.CPUMilli > 0 && !n.RequestsUnknown {
		v.RatiosKnown = true
	}
	if q, ok := s.PressingQueue(); ok {
		v.QueueKnown = true
		v.ShortfallMilli = q.ShortfallMilli()
		v.PreemptionAllowed = q.Allowed
	}
	v.CPUUse = ratio(n.Requested.CPUMilli, n.Allocatable.CPUMilli)
	v.MemUse = ratio(n.Requested.MemBytes, n.Allocatable.MemBytes)
	v.GPUUse = ratio(n.Requested.GPUCount, n.Allocatable.GPUCount)
	return v, nil
}

// resolveCapabilities applies the declared order: the operator knows the deployment, the observer
// knows the cluster, and with neither we assume the upstream exists.
func resolveCapabilities(declared Deployment, s WorldSummary) map[string]Capability {
	out := map[string]Capability{}
	for _, name := range []string{CapPreemption, CapEviction} {
		if c, ok := declared.Capabilities[name]; ok {
			out[name] = c
			continue
		}
		if w, ok := s.Capabilities[name]; ok {
			out[name] = Capability{Exists: w.Exists, Why: w.Source, Declared: true}
			continue
		}
		out[name] = Capability{Exists: true}
	}
	return out
}

func ratio(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total)
}

// Usable says whether the numbers in this view may be used for a decision. It is one predicate so
// that every consumer agrees on what "usable" means: a fresh summary and a known node.
func (v UnitView) Usable() bool {
	return v.SummaryPresent && !v.Stale && v.Node != "" && v.RatiosKnown
}

// Line is the one-line summary the display prints: cluster/node, how full, and what outranks us.
func (v UnitView) Line() string {
	if !v.SummaryPresent {
		return "no observer"
	}
	if v.UnitPresent && v.Node == "" {
		return fmt.Sprintf("%s/%s not placed yet", v.Cluster, v.UnitID)
	}
	if v.Node == "" {
		return v.Cluster + ": unit not tracked"
	}
	out := fmt.Sprintf("%s/%s %d%% cpu", v.Cluster, v.Node, int(v.CPUUse*100+0.5))
	if v.PendingHigherPriority > 0 {
		out += fmt.Sprintf(", %d higher-priority pending", v.PendingHigherPriority)
	}
	if v.Cordoned {
		out += ", cordoned"
	}
	if v.Stale {
		out += fmt.Sprintf(" (STALE %s)", v.Age.Round(time.Second))
	}
	return out
}
