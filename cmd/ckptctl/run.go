package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/conditions"
	"github.com/bolubo/checkpoint-governor/internal/loop"
	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// runRun governs one directory until interrupted.
//
// It is the whole delivery layer in one process: a timer, the loop, and a line per round. There is
// no cluster and no API here on purpose — the controller only needs a directory it can read and one
// file it can write, and a training job that does not know this program exists.
// noUsUsage is printed when the flag has a value that is neither `none` nor a duration. It is not a
// refusal to run without configuration — that is the v1 default — it is a refusal to guess what an
// unparseable answer meant.
const noUsUsage = `--no-us is either none, or a duration like 10m.

       none            you would not have written at all (the governor is the only writer)
       10m             the job writes every 10m if the governor stops answering
       --window=30m    optional: how stale a checkpoint may get while we are alive
`

// parseNoUs turns the flag into the two fields the contract has. "none" is an answer; an empty value
// is not, because "I have not decided" and "I would not have written" are different sentences.
func parseNoUs(v string) (policy.NoUsMode, time.Duration, bool) {
	switch v {
	case "":
		return policy.NoUsUnset, 0, false
	case "none":
		return policy.NoUsNone, 0, true
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return policy.NoUsUnset, 0, false
	}
	return policy.NoUsEvery, d, true
}

func runRun(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "the checkpoint directory to govern")
	unit := fs.String("unit", "", "the unit name (default: the directory)")
	copies := fs.Int("copies", 1, "the smallest number of copies")
	every := fs.Duration("every", 5*time.Second, "how often to look")
	once := fs.Bool("once", false, "take one decision and exit")
	dryRun := fs.Bool("dry-run", false, "observe and decide, then stop without writing anything (plan, not apply)")
	verbose := fs.Bool("verbose", false, "also print the counters, for a bug report")
	freeFactor := fs.Float64("free-factor", 2, "free space below this multiple of one checkpoint is at risk")
	storeEvery := fs.Duration("store-every", 5*time.Second, "how often the storage conditions are read")
	slowFactor := fs.Float64("slow-factor", 2, "how many times the job baseline counts as slow")
	noUs := fs.String("no-us", "", "required: none, or a duration like 10m — what the job does when the governor is gone")
	window := fs.Duration("window", 0, "the longest a checkpoint may stay stale while we are alive (0 = exceptions only)")
	teardown := fs.Duration("teardown", 5*time.Second, "how long the job needs after a final write")
	estimate := fs.Duration("estimate", 0, "how long a write takes before any has been measured (0 = unknown)")
	verifyEvery := fs.Duration("verify-every", 10*time.Minute, "how often one checkpoint may be read back (negative disables)")
	verifyBytes := fs.Int64("verify-bytes-per-hour", 0, "how many bytes may be read back per hour (0 = 2 GiB, negative = unlimited)")
	inflight := fs.Duration("inflight-every", 200*time.Millisecond, "how often to look while a write is running (it is what makes a write's cost measurable)")
	webhook := fs.String("webhook", "", "POST one JSON event to this URL when a plan changes")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ckptctl run --dir=<checkpointDir> [--unit=<name>] [--lost=10m] [--tax=0.05] [--copies=1] [--every=5s] [--once]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fs.Usage()
		return 2
	}
	name := *unit
	if name == "" {
		// The directory's name, not its full path: that is what the world's default unit id is and
		// what the observer's unit identity is built from. A run that sees a world and does not pass
		// --unit must not hash a path nobody published (0030).
		name = filepath.Base(filepath.Clean(*dir))
	}

	// The answers may come from `init` instead of the flags. Flags win, because a person overriding
	// one run should not have to edit a file; the file wins over nothing, because it is the only
	// place an answer can survive the shell that started the job.
	if n, err := advise.LoadNumbers(*dir); err == nil {
		if *noUs == "" {
			*noUs = n.NoUs
		}
		if *window == 0 && n.WithUs != "" {
			if d, derr := time.ParseDuration(n.WithUs); derr == nil {
				*window = d
			}
		}
	}

	// No configuration is a valid configuration (product-v1 §2): unset means `none` — the job
	// would have written nothing — and the start-up line says so out loud. The earlier rule refused to
	// start instead, on the grounds that a silent "no write" is an accident; it is, unless the run
	// states it, which is now the first thing this program prints.
	noUsDeclared := *noUs != ""
	noUsValue := *noUs
	if !noUsDeclared {
		noUsValue = "none"
	}
	noUsMode, noUsEvery, ok := parseNoUs(noUsValue)
	if !ok {
		fmt.Fprint(out, noUsUsage)
		return 2
	}

	// The bound sets the pace: a ten-minute bound does not need a one-second look, and a thirty-second
	// bound cannot afford a ten-second one. An explicit --every wins, because the user may know the
	// job's save cadence better than the formula does.
	cadence := *every
	// Thresholds: the file first, then whatever the operator set on the command line. A flag that
	// was not given must not overwrite the file, which is why the flags that were actually set are
	// collected rather than compared with their defaults.
	probeCfg, fileKeys, err := adapters.LoadConfig(*dir)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 2
	}
	// What the operator declared about this deployment: which upstreams exist at all. It is not a
	// threshold, so it has its own file and its own load.
	if probeCfg.Deployment, err = adapters.LoadDeployment(*dir); err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 2
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "free-factor":
			probeCfg.FreeSpaceFactor = *freeFactor
		case "store-every":
			probeCfg.StoreEvery = *storeEvery
		case "slow-factor":
			probeCfg.SlowFactor = *slowFactor
		}
	})
	// Every override the file made is recorded once: a number nobody remembers changing is the same
	// as a number nobody decided.
	// `from` is the catalogue's number, not the previous run's: we do not keep the last file, and
	// pretending to would be an event that lies about its own precision.
	defaults := adapters.Config{}.Effective()
	for _, key := range fileKeys {
		_ = advise.Record(*dir, advise.Event{
			Kind: advise.EventConfig,
			Code: "threshold-override",
			Values: map[string]any{
				"key": key, "from": defaults[key], "to": probeCfg.Effective()[key],
			},
		})
	}

	// What this deployment adds to the frozen catalogue. A malformed file is a startup failure:
	// governing a job while believing a condition is watched, when it is not, is the one outcome this
	// extension point must not allow.
	extra, err := conditions.Load(*dir)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 2
	}

	// Record what this process actually uses, so the plan can be explained later with the same
	// numbers even when a flag — not the operator's file — set them.
	if err := adapters.WriteThresholds(*dir, probeCfg); err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 1
	}

	noUsText := noUsAnswer(noUsMode, noUsEvery)
	if !noUsDeclared {
		// The first line of a run says which sentence is in force. "unset" is not a detail: it is the
		// difference between a policy the user chose and one this program assumed. It goes inside the
		// existing parenthesis — two parentheses in a row read like a footnote on a footnote.
		noUsText = strings.Replace(noUsText, " (", " (unset — ", 1)
	}
	fmt.Fprintf(out, "watching %s every %s (inflight %s, no-us %s, window %s)\n", *dir, cadence, *inflight, noUsText, windowAnswer(*window))
	if *webhook != "" {
		fmt.Fprintf(out, "notifying %s on every plan change\n", *webhook)
	}
	if extra.Len() > 0 {
		fmt.Fprintf(out, "conditions: %d built-in + %d from disk\n", len(policy.Conditions()), extra.Len())
	}

	host, _ := os.Hostname()
	probeCfg = probeCfg.WithWorld(*dir)
	// The world's unit id is the one this run was given, not the directory's basename: an observer
	// publishes `namespace/pod`, and a probe that hashes the basename looks up a unit nobody wrote.
	// That mismatch made `risk.preempt` blind exactly when a reclaim was waiting for us (0030).
	probeCfg.UnitID = name
	l, err := loop.Open(loop.Config{
		Dir:     *dir,
		UnitID:  name,
		Permit:  permitFor(probeCfg),
		CauseID: causeFor(probeCfg),
		Reclaim: reclaimFor(probeCfg),
		Holder:  fmt.Sprintf("%s-%d", host, os.Getpid()),
		Bounds: policy.Bounds{
			MinCopies: *copies,
			NoUs:      noUsMode,
			NoUsEvery: noUsEvery,
			WithUs:    *window,
		},
		Policy: policy.Risk{Extra: extra.Rows},

		// A one-shot process has no next round to throttle: it must answer the question it was
		// started for, even when the ledger says asking again is not yet worth it.
		AlwaysDecide: *once,
		Window:       *window,
		PlanTTL:      3 * cadence,
		// A dry run observes, decides and reports; it writes nothing at all (see loop.Config.DryRun).
		DryRun: *dryRun,
		// The deadline is the job's own; the teardown and the estimate are ours to declare.
		SecondsLeft: func() (time.Duration, bool) {
			g := adapters.ReadGlue(*dir)
			if !g.HasSeconds {
				return 0, false
			}
			return time.Duration(g.SecondsLeft) * time.Second, true
		},
		// The stop handshake: the job asks, the job says when it is done, and the controller answers.
		StopRequested:      func() bool { return adapters.ReadGlue(*dir).StopRequested },
		JobEnding:          func() bool { return adapters.ReadGlue(*dir).Ending },
		Teardown:           *teardown,
		WriteEstimate:      *estimate,
		VerifyEvery:        *verifyEvery,
		VerifyBytesPerHour: *verifyBytes,
		Probes:             append(adapters.All(probeCfg), extra.Probes...),
		Notify:             webhookPoster(*webhook),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer l.Close()

	// The receipt: what this run did, in the numbers a person can check. Not printed for `--once`,
	// which is a single question and not a run.
	if !*once {
		defer func() {
			fmt.Fprintln(out)
			for _, line := range l.Receipt().Lines() {
				fmt.Fprintln(out, line)
			}
			// Can the newest copy continue the run? The receipt says what the bytes are and what
			// they would restore; "not resumable: optimizer state absent" is the sentence a person
			// needs after a silent death (0026).
			if line := ResumeLine(*dir); line != "" {
				fmt.Fprintln(out, line)
			}
		}()
	}

	// What one write costs here, before the first one happens: the number every later price is built
	// from, and the one a person needs while deciding whether to buy a window at all.
	fmt.Fprintf(out, "governor  one write: %s\n", l.PriceAtStart().Describe())
	if !noUsDeclared && *window == 0 {
		fmt.Fprintln(out, "governor  nothing configured: one write at the end, and one if the platform announces a stop.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// maxFastRounds bounds the "a write should start soon" window: 100 rounds at 200ms is 20s, long
	// enough for any write whose cost matters and short enough to notice an ignored advice.
	lastLine := ""
	const maxFastRounds = 100
	fastRounds := 0

	for {
		// A stop is the normal end of a run, not an abort: the deferred receipt still prints, and a
		// receipt is the only thing that survives a run.
		if ctx.Err() != nil {
			return 0
		}
		st, err := l.Round()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		// One line per decision, in the words of the condition that made it. A round that only observed
		// prints nothing: silence is information, and a log that says something every 200ms is not.
		if st.Decided && !*verbose {
			human := strings.TrimRight(fmt.Sprintf("%s  %-5s %-18s %s",
				time.Now().Format("15:04:05"), st.Action, st.Cause, st.CauseWhy), " ")
			if st.CostHi > 0 {
				human += fmt.Sprintf("  %.1fs", st.CostHi)
			}
			// The same answer every second is not news. A line is printed when the answer changes, or
			// when a write finished and its cost is new information; the totals live in the receipt.
			if human[9:] != lastLine {
				fmt.Fprintln(out, human)
				lastLine = human[9:]
			}
		}
		line := fmt.Sprintf("%s  phase=%-8s action=%-10s decided=%-5v published=%-5v deleted=%d failures=%d",
			time.Now().UTC().Format(time.RFC3339), st.Phase, st.Action, st.Decided, st.Published, len(st.Deleted), st.Failures)
		if st.ReadOnly {
			line += "  (read-only: another controller holds the lease)"
		}
		if st.CostHi > 0 {
			line += fmt.Sprintf("  write=[%.2fs, %.2fs]", st.CostLo, st.CostHi)
		}
		line += fmt.Sprintf("  scan=%.2fms reads=%d stats=%d fsync=%d",
			float64(st.Overhead.ScanNS)/1e6, st.Overhead.ManifestReads, st.Overhead.StatCalls, st.Overhead.Fsyncs)
		if *verbose || *once {
			fmt.Fprintln(out, line)
		}
		if *once {
			if *dryRun {
				fmt.Fprintln(out, dryRunLine(st))
			}
			return 0
		}
		// Look far more often from the moment a save is published, not from the moment the write is
		// first seen: a write shorter than the poll interval would otherwise be born and finish
		// between two sightings, and its cost would be unmeasurable. The fast window is bounded, so
		// a job that ignores the advice does not turn the controller into a busy loop.
		fast := st.Phase == advise.PhaseWriting || (st.Published && (st.Action == policy.ActionSave || st.Action == policy.ActionRepair))
		if fast {
			fastRounds++
		} else {
			fastRounds = 0
		}
		delay := cadence
		if fast && fastRounds <= maxFastRounds && *inflight > 0 && *inflight < delay {
			delay = *inflight
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(delay):
		}
	}
}

// dryRunLine is the preview: what a round would have done, said in the past tense of a thing that
// did not happen. It always names the two acts that cannot be undone — a published plan the job
// would act on, and a retired copy — because those are what a person is being asked to approve.
func dryRunLine(st loop.Step) string {
	parts := []string{"dry run: nothing was written"}
	if st.Decided {
		if st.Action == policy.ActionSave || st.Action == policy.ActionRepair {
			parts = append(parts, fmt.Sprintf("would publish %s (%s)", st.Action, st.Cause))
		} else {
			parts = append(parts, fmt.Sprintf("would publish skip (%s)", st.Cause))
		}
	} else {
		parts = append(parts, "no decision this round (nothing it reads changed)")
	}
	if len(st.Deleted) > 0 {
		steps := make([]string, 0, len(st.Deleted))
		for _, d := range st.Deleted {
			steps = append(steps, strconv.FormatInt(d.Step, 10))
		}
		parts = append(parts, "would retire "+strings.Join(steps, ", ")+" (nothing was moved)")
	}
	return strings.Join(parts, " · ")
}

// noUsAnswer and windowAnswer are how the start-up line says the two numbers: a person has to be
// able to read what the controller will do, and "0s" is not an answer.
func noUsAnswer(mode policy.NoUsMode, every time.Duration) string {
	switch mode {
	case policy.NoUsNone:
		return "none (no writes if the governor stops)"
	case policy.NoUsEvery:
		return "the job writes every " + every.String()
	default:
		return "unanswered"
	}
}

func windowAnswer(w time.Duration) string {
	if w <= 0 {
		return "off (exceptions only)"
	}
	// "2h0m0s" is a duration literal, not a sentence. The start-up line is read by a person.
	return shortDuration(w)
}

// permitFor reads the fleet's verdict for this unit (0021), fail-open.
//
// Every failure is permission: no observer, no unit record, no permit field, a verdict word this
// build does not know, a queue whose end has passed. A coordination channel that can suppress a save
// fails exactly when the fleet is under pressure, so it is not allowed to fail in that direction.
func permitFor(cfg adapters.Config) func(time.Time) (permit.Verdict, time.Time) {
	return func(now time.Time) (permit.Verdict, time.Time) {
		u, err := adapters.ReadUnit(cfg.WorldDir, cfg.Cluster, cfg.UnitID)
		if err != nil || u.Permit == nil {
			return permit.Granted, time.Time{}
		}
		var until time.Time
		if u.Permit.Until != nil {
			until = *u.Permit.Until
		}
		return permit.Parse(u.Permit.Verdict, until, now), until
	}
}

// heldReclaim is the one read both the cause id and the executor's "this is a gate deadline"
// question use. It returns the event id (`reclaim:<requestId>`), not the bare request id, because
// the ledger compares it with what it already paid for.
func heldReclaim(cfg adapters.Config, now time.Time) (string, bool) {
	rec, held, err := adapters.ReadHeldReclaimForUnit(cfg.WorldDir, cfg.Cluster, cfg.UnitID, now)
	if err != nil || !held {
		return "", false
	}
	return "reclaim:" + rec.RequestID, true
}

// reclaimFor tells the loop that a gate is waiting on this unit. A held reclaim is a new event with
// a deadline we do not own, so the loop must be able to ask now and the executor must not apply the
// ordinary cooldown to the write it asks for (0030).
func reclaimFor(cfg adapters.Config) func(time.Time) (string, bool) {
	return func(now time.Time) (string, bool) { return heldReclaim(cfg, now) }
}

// causeFor names the event that puts this unit at risk, for the one-decision-per-event rule (0022).
//
// It reads one cluster-level fact — "higher-priority work is waiting for room" — and not the
// scheduler's decision, which this product does not integrate with and cannot see. While the same
// work waits the id is the same, so the unit knows the event has already been paid for.
//
// Empty means the observer did not name one, and the gate then writes as it always did: an event
// that cannot be identified must never be the reason a save does not happen.
func causeFor(cfg adapters.Config) func(time.Time) string {
	return func(now time.Time) string {
		// A held reclaim is an event of its own, not a repeat of the waiting work the observer already
		// named. It asks for a seal *now*, on behalf of an arrival that is deliberately absent from the
		// world, and reusing the old event id would let the ledger dedupe the only write the gate is
		// waiting for (0030).
		if id, held := heldReclaim(cfg, now); held {
			return id
		}
		s, err := adapters.ReadSummary(cfg.WorldDir, cfg.Cluster)
		if err != nil {
			return ""
		}
		var mine int32
		if u, err := adapters.ReadUnit(cfg.WorldDir, cfg.Cluster, cfg.UnitID); err == nil {
			mine = u.Priority
		}
		// The identity is the arrivals that outrank *this* unit: a unit cannot be taken to make room for
		// work that ranks below it, so those arrivals are not its event (0022).
		return adapters.CauseID(s.Arrivals, mine)
	}
}
