package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/observe"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// runExplain turns the published plan into the one thing a person asks for: why did my job just
// checkpoint (or not).
//
// It reads only the state file, so the answer is exactly what the job was told — not a reconstruction
// from a log that might have rotated.
func runExplain(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	dir := fs.String("dir", "", "the checkpoint directory")
	asJSON := fs.Bool("json", false, "print the decision as one JSON object")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprint(out, "usage: ckptctl explain --dir=<checkpointDir>\n")
		return 2
	}

	d, ok := advise.LastDecision(*dir)
	if !ok {
		if *asJSON {
			problem := advise.SchemaProblem(*dir)
			writeJSON(out, explainPayload{Schema: 1, Dir: *dir, Error: problem})
			return 1
		}
		// Refusing a foreign schema silently would be the same failure as reading it wrongly: the two
		// states lead to the same fallback, but only one of them is something a person can fix.
		if problem := advise.SchemaProblem(*dir); problem != "" {
			fmt.Fprintln(out, problem)
			return 1
		}
		fmt.Fprintf(out, "no plan in %s: the controller has not published one yet\n", *dir)
		return 1
	}

	if *asJSON {
		writeJSON(out, explainPayload{Schema: 1, Dir: *dir, Decision: decisionPayload(d)})
		return 0
	}

	fmt.Fprintf(out, "decision   %s", d.Action)
	if d.Reason != "" {
		fmt.Fprintf(out, " (%s)", d.Reason)
	}
	fmt.Fprintln(out)

	if d.Why != "" {
		fmt.Fprintf(out, "why        %s\n", d.Why)
	}
	if len(d.Reasons) > 0 {
		for _, r := range d.Reasons {
			fmt.Fprintf(out, "reason     %s\n", reasonSentence(enrich(*dir, r)))
		}
	}
	if len(d.Missing) > 0 {
		fmt.Fprintf(out, "missing    %s\n", strings.Join(d.Missing, ", "))
	}
	if len(d.Sensitivity) > 0 {
		for _, s := range d.Sensitivity {
			fmt.Fprintf(out, "sensitive  %s at %g → %s\n", s.Field, s.Threshold, s.WouldBe)
		}
	}
	fmt.Fprintf(out, "write      %s, keep %d\n", kindOf(d), d.KeepCopies)

	// The world behind the decision: which node, how old the reading is, and whether the answer was
	// an announcement or a guess. A person deciding whether to trust it needs all three.
	worldBlock(out, *dir, time.Now())
	return 0
}

func kindOf(d policy.Decision) string {
	if d.Kind != "" {
		return string(d.Kind)
	}
	return "full"
}

type explainPayload struct {
	Schema   int              `json:"schema"`
	Dir      string           `json:"dir"`
	Decision *explainDecision `json:"decision,omitempty"`
	Error    string           `json:"error,omitempty"`
}

type explainDecision struct {
	Action      string               `json:"action"`
	Reason      string               `json:"reason,omitempty"`
	Why         string               `json:"why,omitempty"`
	Reasons     []explainReason      `json:"reasons,omitempty"`
	Missing     []string             `json:"missing,omitempty"`
	Sensitivity []explainSensitivity `json:"sensitivity,omitempty"`
	Kind        string               `json:"kind"`
	Keep        int                  `json:"keep"`
}

type explainReason struct {
	Code     string             `json:"code"`
	Values   map[string]float64 `json:"values,omitempty"`
	Evidence []string           `json:"evidence,omitempty"`
}

type explainSensitivity struct {
	Field     string  `json:"field"`
	Threshold float64 `json:"threshold"`
	Direction string  `json:"direction"`
	WouldBe   string  `json:"wouldBe"`
}

func decisionPayload(d policy.Decision) *explainDecision {
	out := &explainDecision{
		Action: string(d.Action), Reason: string(d.Reason), Why: d.Why,
		Missing: d.Missing, Kind: kindOf(d), Keep: d.KeepCopies,
	}
	for _, r := range d.Reasons {
		out.Reasons = append(out.Reasons, explainReason{Code: r.Code, Values: r.Values, Evidence: r.Evidence})
	}
	for _, s := range d.Sensitivity {
		out.Sensitivity = append(out.Sensitivity, explainSensitivity{
			Field: s.Field, Threshold: s.Threshold, Direction: s.Direction, WouldBe: s.WouldBe,
		})
	}
	return out
}

// enrich fills in the numbers a reason does not carry, by asking the producer that measured them.
//
// The decision is right to leave `values` empty: the algorithm sees booleans, and a number it never
// read is a number it must not print. But a person asking "why" wants the sizes, and they exist —
// one layer down, in the adapter, against the same directory and the same threshold. So the
// explanation goes and gets them instead of pretending the reason is complete.
func enrich(dir string, r policy.ReasonEntry) policy.ReasonEntry {
	if r.Code != "store.low-space" || r.Values["need"] > 0 {
		return r
	}
	factor, ok := adapters.RecordedFreeFactor(dir)
	if !ok {
		cfg, _, err := adapters.LoadConfig(dir)
		if err != nil {
			return r
		}
		factor = cfg.FreeFactor()
	}
	snap, _, err := observe.Scan(observe.Request{Dir: dir, UnitID: dir, Now: time.Now()})
	if err != nil {
		return r
	}
	free, need, ok := adapters.LowSpaceNumbers(dir, snap.CheckpointBytes, factor)
	if !ok {
		return r
	}
	r.Values = map[string]float64{"free": float64(free), "need": float64(need), "factor": factor}
	return r
}

// reasonSentence turns one machine-readable reason into the sentence a person needs.
//
// The numbers are the point: "store.low-space fired" says which code, while "free 1.9GB < need
// 2.0GB (2 × 1.0GB)" says why, and the second is what lets somebody decide whether to delete
// something or raise the limit. Unknown codes fall back to a code-plus-values form rather than
// inventing prose for a condition this build has never seen.
func reasonSentence(r policy.ReasonEntry) string {
	switch r.Code {
	case "store.low-space":
		free, need, factor := r.Values["free"], r.Values["need"], r.Values["factor"]
		if need <= 0 {
			// The policy names the condition without the sizes behind it, which is a known gap. Say
			// what is true and stop: inventing a comparison that was never made is worse than a
			// shorter sentence.
			return "store.low-space fired; free space is below the checkpoint scale"
		}
		s := fmt.Sprintf("store.low-space fired; free %s < need %s",
			humanBytes(int64(free)), humanBytes(int64(need)))
		if factor >= 1 {
			s += fmt.Sprintf(" (%d × %s)", int(factor), humanBytes(int64(need/factor)))
		}
		return s
	case "store.blocked":
		return "store.blocked fired; the write target refuses writes right now, so writing cannot help"
	case "job.ending":
		return "job.ending fired; training is finishing, so this is the last useful moment"
	case "risk.shutdown":
		return "risk.shutdown fired; the job has been told to stop and is spending its grace period"
	case "risk.preempt":
		return "risk.preempt fired; higher-priority work is taking these resources"
	case "host.memory-pressure":
		return "host.memory-pressure fired; the kernel is about to kill the job for memory"
	case "host.cpu-throttled":
		return "host.cpu-throttled fired; the CPU quota is throttling the job"
	case "risk.eviction-likely":
		by := strings.TrimSpace(strings.Join(r.Evidence, ", "))
		if by == "" {
			by = "an inference without a readable signal"
		}
		return "risk.eviction-likely fired; the node is filling up (" + by + ")"
	}
	if len(r.Values) == 0 {
		return r.Code + " fired"
	}
	return r.Code + " fired" + formatValues(r.Values)
}

func formatValues(m map[string]float64) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%g", k, m[k]))
	}
	return " (" + strings.Join(parts, " ") + ")"
}

func from(evidence []string) string {
	if len(evidence) == 0 {
		return ""
	}
	return " from " + strings.Join(evidence, ", ")
}
