package advise

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Phase is where a unit is in the write lifecycle (design, "The write lifecycle").
//
// It is the implementation's vocabulary, not the contract's: the job reads an action, a person reads
// a phase. Keeping them apart is what lets the view say "writing, 45%, 2m10s" without inventing a
// field in the published file.
type Phase string

const (
	PhaseIdle    Phase = "idle"
	PhaseDue     Phase = "due"
	PhaseWriting Phase = "writing"
	PhaseSealed  Phase = "sealed"
	PhaseBroken  Phase = "broken"
	PhaseStalled Phase = "stalled"
	PhaseBlocked Phase = "blocked"
)

// WriteRecord is one finished write, as it is shown in the history.
type WriteRecord struct {
	Step      int64   `json:"step"`
	Bytes     int64   `json:"bytes"`
	State     string  `json:"state"` // sealed or broken
	StartedAt int64   `json:"startedAt"`
	EndedAt   int64   `json:"endedAt"`
	Seconds   float64 `json:"seconds"`
}

// Failure is one structured failure. The code is for machines; the detail is for people.
type Failure struct {
	Code   string `json:"code"`
	At     int64  `json:"at"`
	Detail string `json:"detail"`
}

// Status is what a person asked for: what is happening now, and what happened last.
//
// Every time in it is absolute. Anything relative ("2m10s ago") is computed when the view is
// rendered, because a stored relative time ages the moment it is written and a file that always
// looks fresh is a file nobody trusts.
type Status struct {
	UnitID     string `json:"unitId"`
	Generation int64  `json:"generation"`
	Phase      Phase  `json:"phase"`

	// Conditions is the latest reading per condition. The value is spelled the way the producer
	// gave it ("1", "0", "absent", "unknown"); since is when it started saying that, so a person can
	// tell a condition that has been true for two minutes from one that just flipped; reason carries
	// the probe's error when the answer is "I cannot see".
	Conditions map[string]Condition `json:"conditions,omitempty"`

	// Attempts are the most recent write attempts, oldest first. They are the record of "what did
	// we ask for and what happened", which no count of sealed directories can answer.
	Attempts []AttemptLog `json:"attempts,omitempty"`

	// Blind is how many conditions answered "unknown" in the last round. It is a count, not a copy:
	// the names and the reasons are in Conditions. WatchBroken rides there as the watch.broken
	// condition, because it is one fact with one representation.
	Blind int `json:"blind,omitempty"`
	// LastGate is why the gate last stopped a write: "cooldown", "headroom", "duplicate",
	// "backoff", "in-flight", "not-a-save" or "volume-busy". It is the answer to "why is nothing
	// being written" that a person asks first.
	LastGate string `json:"lastGate,omitempty"`
	// GateDetail is the sentence behind that code when the code alone is not enough: the sizes a
	// headroom refusal compared, or the deadline arithmetic a no-time refusal did.
	GateDetail string `json:"gateDetail,omitempty"`

	SealStep int64 `json:"sealStep"`
	SealAt   int64 `json:"sealAt"`

	WriteStep      int64   `json:"writeStep"`
	WriteStartedAt int64   `json:"writeStartedAt"`
	WriteProgress  float64 `json:"writeProgress"` // -1 unknown
	WriteExpected  float64 `json:"writeExpected"` // seconds, 0 unknown

	// EstimateSeconds is how long one write is expected to take, and EstimateSource says where the
	// number came from: "measured", "job", "probe" or "unknown". An estimate is never a measurement;
	// the status must not blur the two, because "we have measured this" is a promise.
	EstimateSeconds float64 `json:"estimateSeconds,omitempty"`
	EstimateSource  string  `json:"estimateSource,omitempty"`

	AdviceAt     int64  `json:"adviceAt"` // when the last plan was published
	SeenAt       int64  `json:"seenAt"`   // when the directory first showed the advised write
	AdviceAction string `json:"adviceAction,omitempty"`
	AdviceReason string `json:"adviceReason,omitempty"`

	Writes   []WriteRecord `json:"writes"`
	Failures int           `json:"failures"`
	Last     *Failure      `json:"lastFailure,omitempty"`

	// Overhead is cumulative: what this controller has cost so far.
	Overhead Overhead `json:"overhead,omitempty"`
	// VerifyDeferred counts verifications the byte budget pushed to a later window. It is how a
	// person sees that "nothing is broken" has become "we have not looked recently".
	VerifyDeferred int `json:"verifyDeferred,omitempty"`
	// NewestUnverifiedAt is when the newest copy that has never been read back was first seen. Zero
	// means there is none: either every sealed copy has been looked at, or there is no copy at all.
	// "Never looked" and "looked a minute ago" are different answers, and this is how they stay apart.
	NewestUnverifiedAt int64 `json:"newestUnverifiedAt,omitempty"`
	// VerifyBudgetBytes is the hourly read-back allowance and VerifyUsedBytes what this window has
	// spent. A budget nobody prints is a budget nobody knows is exhausted.
	VerifyBudgetBytes int64 `json:"verifyBudgetBytes,omitempty"`
	VerifyUsedBytes   int64 `json:"verifyUsedBytes,omitempty"`

	// The two numbers as the run was configured, so a report can compute the saving without asking
	// the configuration again: a directory outlives the process that governed it.
	BaselineSeconds int64 `json:"baselineSeconds,omitempty"`
	BaselineNone    bool  `json:"baselineNone,omitempty"`
	WindowSeconds   int64 `json:"windowSeconds,omitempty"`

	// The window as the executor holds it: when it closes, when a seal last restarted it, and
	// whether it cannot hold because one write is longer than the whole span.
	WindowSpanSeconds int64 `json:"windowSpanSeconds,omitempty"`
	WindowDueAt       int64 `json:"windowDueAt,omitempty"`
	WindowRefreshedAt int64 `json:"windowRefreshedAt,omitempty"`
	WindowInfeasible  bool  `json:"windowInfeasible,omitempty"`

	// StartedAt is the first round's time: the run's clock. LastRoundAt is the newest one, so
	// "are we here" is a fact a person can check without trusting the shell prompt.
	StartedAt   int64 `json:"startedAt,omitempty"`
	LastRoundAt int64 `json:"lastRoundAt,omitempty"`

	// Kinds counts finished writes by why they happened: "window" (the span closed), "exception" (a
	// condition fired), "final" (training ended), "repair" (a broken copy). The final write is
	// excluded from the saving, because everybody writes it.
	Kinds map[string]int `json:"writeKinds,omitempty"`
	// LastReceipt is what the most recent sealed write bought: the staleness when it was requested,
	// the staleness it replaced, and how long it took.
	LastReceipt *Receipt `json:"lastReceipt,omitempty"`

	// FinalAskedAt is when the final write was asked for, and FinalOutcome what became of it:
	// "asked" (still running), "sealed at step N", "already on disk at the current step", or
	// "not made: <reason>". A final write nobody can make is the run's failed final write: it is the one
	// line that must be read after a job ends, so it is stored as a sentence instead of a code.
	FinalAskedAt int64  `json:"finalAskedAt,omitempty"`
	FinalOutcome string `json:"finalOutcome,omitempty"`
	// Failure is the named state when something is wrong: blind, read-only, refused, abandoned,
	// broken, final-write-failed, baseline-unset. Empty means nothing is wrong.
	Failure string `json:"failure,omitempty"`
}

// Receipt is one finished write, priced in the only currency that matters: how much work was at
// risk when it was asked for, and how much is at risk now that it has landed.
type Receipt struct {
	Kind           string  `json:"kind"`
	StaleAtRequest float64 `json:"staleAtRequest"`
	StaleAtSeal    float64 `json:"staleAtSeal"`
	Cost           float64 `json:"cost"`
}

// secondsToDuration is how a stored number of seconds becomes something short() can print.
func secondsToDuration(sec float64) time.Duration {
	return time.Duration(sec * float64(time.Second))
}

// windowLine is the answer to "if it happened now, could we cover it": the user's span, when it
// closes, and how much of it is left.
func windowLine(s Status, now time.Time) string {
	if s.WindowSpanSeconds == 0 {
		return "window          off (exceptions only) · nothing time-based will write\n"
	}
	span := short(time.Duration(s.WindowSpanSeconds) * time.Second)
	switch {
	case s.WindowInfeasible:
		return fmt.Sprintf("window          %s — shorter than one write: this bound cannot hold\n", span)
	case s.WindowDueAt == 0:
		return fmt.Sprintf("window          %s (not armed yet: nothing has been sealed)\n", span)
	default:
		due := time.Unix(s.WindowDueAt, 0)
		left := due.Sub(now)
		when := "in " + short(left)
		if left < 0 {
			when = "overdue by " + short(-left)
		}
		refreshed := ""
		if s.WindowRefreshedAt != 0 {
			refreshed = " · last refresh " + short(now.Sub(time.Unix(s.WindowRefreshedAt, 0))) + " ago"
		}
		return fmt.Sprintf("window          %s · due %s (%s)%s\n",
			span, due.UTC().Format("15:04:05"), when, refreshed)
	}
}

// savingLine is the gap between the two numbers, computed from what actually happened. It is
// evidence, not a promise: the baseline is the number the user stated as his own habit, and the
// final write (which everybody makes) is excluded.
func savingLine(s Status, now time.Time) string {
	elapsed := now.Sub(time.Unix(s.StartedAt, 0))
	if elapsed <= 0 {
		return ""
	}
	windowN, exc := s.Kinds["window"], s.Kinds["exception"]
	actual := windowN + exc
	switch {
	case s.BaselineNone || s.BaselineSeconds == 0:
		return fmt.Sprintf("saving          no-us none: you would not have written; we wrote %d\n", actual)
	case s.WindowSeconds == 0:
		base := int(elapsed / (time.Duration(s.BaselineSeconds) * time.Second))
		return fmt.Sprintf("saving          no-us %s → %d writes · with-us off · wrote %d (exceptions only) · saved %d\n",
			short(time.Duration(s.BaselineSeconds)*time.Second), base, actual, base-actual)
	default:
		base := int(elapsed / (time.Duration(s.BaselineSeconds) * time.Second))
		win := int(elapsed / (time.Duration(s.WindowSeconds) * time.Second))
		saved := base - actual
		pct := 0.0
		if base > 0 {
			pct = float64(saved) / float64(base) * 100
		}
		return fmt.Sprintf("saving          no-us %s → %d · with-us %s → %d · wrote %d (%d window + %d exception) · saved %d (%.0f%%)\n",
			short(time.Duration(s.BaselineSeconds)*time.Second), base,
			short(time.Duration(s.WindowSeconds)*time.Second), win,
			actual, windowN, exc, saved, pct)
	}
}

// Overhead is what the governor itself cost. The product asks the trainer to keep its write tax
// under a bound; this is the same demand made of us, in the units that can be acted on.
//
// Every counter is incremented where the work happens, not derived from the code: a number computed
// from what the code is supposed to do stops being true the first time the code changes.
type Overhead struct {
	ScanNS    int64 `json:"scanNs,omitempty"`
	PublishNS int64 `json:"publishNs,omitempty"`
	VerifyNS  int64 `json:"verifyNs,omitempty"`
	DeleteNS  int64 `json:"deleteNs,omitempty"`

	StatCalls     int `json:"statCalls,omitempty"`
	ManifestReads int `json:"manifestReads,omitempty"`
	Fsyncs        int `json:"fsyncs,omitempty"`
	// DeletedDirs counts copies retired (renamed into .trash); SweptDirs counts the ones actually
	// unlinked later. They differ because removal is split in two to keep a round short.
	DeletedDirs int `json:"deletedDirs,omitempty"`
	SweptDirs   int `json:"sweptDirs,omitempty"`

	VerifyBytes int64 `json:"verifyBytes,omitempty"`

	// Rounds and Decisions are the two cadences the loop keeps apart: observation is cheap and
	// frequent, decisions are free but should be rare.
	Rounds    int `json:"rounds,omitempty"`
	Decisions int `json:"decisions,omitempty"`

	// Signals is how many times each declared capability has been read. It is cumulative and is
	// assigned, not added: the count lives in the reader.
	Signals map[string]int `json:"signals,omitempty"`
}

// Status carries the conditions and the attempts: they are what the controller is doing, not what
// observing it cost.

// Condition is one condition's answer, with how long it has been saying it and why, when the answer
// is that it cannot see.
type Condition struct {
	Value  string `json:"value"`
	Since  int64  `json:"since,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// AttemptLog is one attempt as the view prints it.
type AttemptLog struct {
	ID        string    `json:"id"`
	Trigger   string    `json:"trigger"`
	Outcome   string    `json:"outcome"`
	StartedAt time.Time `json:"startedAt"`
}

// Add accumulates one round.
func (o *Overhead) Add(p Overhead) {
	o.ScanNS += p.ScanNS
	o.PublishNS += p.PublishNS
	o.VerifyNS += p.VerifyNS
	o.DeleteNS += p.DeleteNS
	o.StatCalls += p.StatCalls
	o.ManifestReads += p.ManifestReads
	o.Fsyncs += p.Fsyncs
	o.DeletedDirs += p.DeletedDirs
	o.SweptDirs += p.SweptDirs
	o.VerifyBytes += p.VerifyBytes
	o.Rounds += p.Rounds
	o.Decisions += p.Decisions
}

// HistoryLimit is how many finished writes the view keeps.
const HistoryLimit = 10

// Remember appends a finished write and keeps the history bounded.
func (s *Status) Remember(r WriteRecord) {
	s.Writes = append(s.Writes, r)
	if len(s.Writes) > HistoryLimit {
		s.Writes = s.Writes[len(s.Writes)-HistoryLimit:]
	}
}

// Render writes the human view. Relative durations are computed here, from now, and nowhere else.
func Render(w io.Writer, s Status, now time.Time, bound time.Duration) {
	fmt.Fprintf(w, "unit %s   generation %d   phase %s\n", orDash(s.UnitID), s.Generation, orDash(string(s.Phase)))

	if s.SealAt == 0 {
		fmt.Fprintln(w, "newest sealed   none")
	} else {
		seal := time.Unix(s.SealAt, 0)
		age := short(now.Sub(seal))
		if bound > 0 {
			fmt.Fprintf(w, "newest sealed   step %d at %s (age %s, bound %s)\n",
				s.SealStep, seal.UTC().Format(time.RFC3339), age, short(bound))
		} else {
			// No bound is not a zero: the product asks for one number, and the age is a fact.
			fmt.Fprintf(w, "newest sealed   step %d at %s (age %s, nothing to compare it with)\n",
				s.SealStep, seal.UTC().Format(time.RFC3339), age)
		}
	}

	if s.WriteStartedAt == 0 {
		fmt.Fprintln(w, "writing         none")
	} else {
		started := time.Unix(s.WriteStartedAt, 0)
		line := fmt.Sprintf("writing         step %d", s.WriteStep)
		if s.WriteProgress >= 0 {
			line += fmt.Sprintf(", %d%%", int(s.WriteProgress*100+0.5))
		}
		line += fmt.Sprintf(", started %s, %s elapsed", started.UTC().Format(time.RFC3339), short(now.Sub(started)))
		if s.WriteExpected > 0 {
			line += fmt.Sprintf(", %s expected", short(time.Duration(s.WriteExpected*float64(time.Second))))
		}
		fmt.Fprintln(w, line)
	}

	if s.EstimateSeconds > 0 {
		fmt.Fprintf(w, "write estimate  %s (%s)\n",
			short(time.Duration(s.EstimateSeconds*float64(time.Second))), s.EstimateSource)
	}

	if s.AdviceAt == 0 {
		fmt.Fprintln(w, "advice          none")
	} else {
		line := fmt.Sprintf("advice          published %s", time.Unix(s.AdviceAt, 0).UTC().Format(time.RFC3339))
		if s.SeenAt > 0 {
			line += fmt.Sprintf(", seen in the directory %s", time.Unix(s.SeenAt, 0).UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(w, line)
	}

	if s.SealAt != 0 {
		line := "verify          "
		if s.NewestUnverifiedAt == 0 {
			line += "every sealed copy has been read back"
		} else {
			line += "newest unverified " + short(now.Sub(time.Unix(s.NewestUnverifiedAt, 0)))
		}
		if s.VerifyDeferred > 0 {
			line += fmt.Sprintf(", %d deferred", s.VerifyDeferred)
		}
		if s.VerifyBudgetBytes > 0 {
			line += fmt.Sprintf(" · %s used of %s/hour", formatBytes(s.VerifyUsedBytes), formatBytes(s.VerifyBudgetBytes))
		}
		fmt.Fprintln(w, line)
	}

	if len(s.Writes) == 0 {
		fmt.Fprintln(w, "last writes     none")
	} else {
		line := "last writes     "
		for i, r := range s.Writes {
			if i > 0 {
				line += " | "
			}
			if r.State == "broken" {
				line += fmt.Sprintf("step %d broken", r.Step)
				continue
			}
			line += fmt.Sprintf("step %d sealed %s %s", r.Step, short(time.Duration(r.Seconds*float64(time.Second))), formatBytes(r.Bytes))
		}
		fmt.Fprintln(w, line)
	}

	if s.Failures == 0 {
		fmt.Fprintln(w, "failures        0")
	} else {
		line := fmt.Sprintf("failures        %d", s.Failures)
		if s.Last != nil {
			line += fmt.Sprintf(": %s (%s) at %s", s.Last.Code, s.Last.Detail, time.Unix(s.Last.At, 0).UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(w, line)
	}

	o := s.Overhead
	line := fmt.Sprintf("overhead        scan %s · reads %d · stats %d · fsync %d · verify %s",
		precise(time.Duration(o.ScanNS)), o.ManifestReads, o.StatCalls, o.Fsyncs, formatBytes(o.VerifyBytes))
	if s.VerifyDeferred > 0 {
		line += fmt.Sprintf(" (deferred %d)", s.VerifyDeferred)
	}
	line += fmt.Sprintf(" · deleted %d", o.DeletedDirs)
	if o.SweptDirs > 0 {
		line += fmt.Sprintf(" (swept %d)", o.SweptDirs)
	}
	line += fmt.Sprintf(" · rounds %d, decisions %d", o.Rounds, o.Decisions)
	fmt.Fprintln(w, line)

	if len(s.Conditions) > 0 {
		names := make([]string, 0, len(s.Conditions))
		for name := range s.Conditions {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			c := s.Conditions[name]
			part := name + " " + c.Value
			// A reason is what a person needs when the answer is "I cannot see"; an age is what
			// matters when a condition is true, because "true for two minutes" and "just flipped"
			// lead to different decisions about whether to trust it.
			switch {
			case c.Reason != "":
				part += " (" + c.Reason + ")"
			case c.Value == "1" && c.Since > 0 && now.Unix() >= c.Since:
				part += " (" + short(time.Duration(now.Unix()-c.Since)*time.Second) + ")"
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(w, "conditions      %s\n", strings.Join(parts, " · "))
	}

	if n := len(s.Attempts); n > 0 {
		from := 0
		if n > 3 {
			from = n - 3
		}
		parts := make([]string, 0, n-from)
		for _, a := range s.Attempts[from:] {
			outcome := a.Outcome
			if outcome == "" {
				outcome = "running"
			}
			parts = append(parts, outcome+"/"+a.Trigger)
		}
		fmt.Fprintf(w, "attempts        %d total · last: %s\n", n, strings.Join(parts, ", "))
	}

	// Blindness and the last refusal are what a person asks about second, after the counts: one
	// says "we cannot see", the other says "we chose not to write, and here is why".
	if s.Blind > 0 || s.LastGate != "" {
		var parts []string
		if s.Blind > 0 {
			parts = append(parts, fmt.Sprintf("blind %d", s.Blind))
		}
		if s.LastGate != "" {
			parts = append(parts, "last gate "+s.LastGate)
		}
		fmt.Fprintf(w, "watch           %s\n", strings.Join(parts, " · "))
	}

	// The sentence behind the code. "no-time" is not actionable on its own; the arithmetic is.
	if s.GateDetail != "" {
		fmt.Fprintf(w, "gate            %s\n", s.GateDetail)
	}

	// The three questions a person asks in this order: are we here, why is nothing written, and if
	// it happened now, could we cover it. The window answers the third one; the presence line and
	// the gate detail answer the first two.
	if s.LastRoundAt != 0 || s.WindowSpanSeconds != 0 {
		parts := []string{}
		if s.LastRoundAt != 0 {
			parts = append(parts, "last round "+short(now.Sub(time.Unix(s.LastRoundAt, 0)))+" ago")
		}
		if n := len(s.Conditions); n > 0 {
			parts = append(parts, fmt.Sprintf("watching %d conditions", n))
		}
		parts = append(parts, fmt.Sprintf("%d blind", s.Blind))
		fmt.Fprintf(w, "presence        %s\n", strings.Join(parts, " · "))
	}
	fmt.Fprint(w, windowLine(s, now))

	if s.LastReceipt != nil {
		r := s.LastReceipt
		fmt.Fprintf(w, "receipt         stale %s → %s, cost %s (%s)\n",
			short(secondsToDuration(r.StaleAtRequest)), short(secondsToDuration(r.StaleAtSeal)),
			short(secondsToDuration(r.Cost)), r.Kind)
	}
	if len(s.Kinds) > 0 {
		fmt.Fprintf(w, "writes          window %d · exception %d · final %d · repair %d\n",
			s.Kinds["window"], s.Kinds["exception"], s.Kinds["final"], s.Kinds["repair"])
	}
	if s.FinalOutcome != "" {
		asked := ""
		if s.FinalAskedAt != 0 && now.Unix() >= s.FinalAskedAt {
			asked = "asked " + short(now.Sub(time.Unix(s.FinalAskedAt, 0))) + " ago · "
		}
		fmt.Fprintf(w, "final           %s%s\n", asked, s.FinalOutcome)
	}
	if s.Failure != "" {
		fmt.Fprintf(w, "failure         %s\n", s.Failure)
	}
	if s.StartedAt != 0 {
		fmt.Fprint(w, savingLine(s, now))
	}

	if len(o.Signals) > 0 {
		names := make([]string, 0, len(o.Signals))
		for name := range o.Signals {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, fmt.Sprintf("%s %d", name, o.Signals[name]))
		}
		fmt.Fprintf(w, "signals         %s\n", strings.Join(parts, " · "))
	}
}

// OutputLine says what the next write should produce, in the words a person uses.
//
// It is deliberately separate from the view of the ledger: this is about the plan, not about what
// the controller has done, and the plan lives in the published file.
func OutputLine(d policy.Decision) string {
	kind := string(d.Kind)
	if kind == "" {
		kind = "full"
	}
	parts := []string{kind}
	return strings.Join(parts, " · ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// precise keeps sub-second figures legible: a scan that costs 0.16ms rendered as "0s" hides the only
// number the overhead line exists for.
func precise(d time.Duration) string {
	if d < time.Second && d >= 0 {
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	}
	return short(d)
}

// short renders a duration to the second: the view is read by people, not parsed.
func short(d time.Duration) string {
	if d < 0 {
		return "-" + short(-d)
	}
	return d.Round(time.Second).String()
}

func formatBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	value := float64(n)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, u := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.0f%s", value, u)
		}
	}
	return fmt.Sprintf("%.0fPB", value)
}
