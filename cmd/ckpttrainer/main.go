// Command ckpttrainer is the five-line integration from the design, written out: it trains, it
// writes the three small files the controller reads, and at each save point it obeys the plan in
// state.json.
//
// It exists to prove the delivery layer end to end without a cluster: a real process, real files,
// real bytes, and a trace that can be replayed offline against the same policy.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/verify"
)

type config struct {
	dir        string
	steps      int
	stepMS     int
	writeMS    int
	shardBytes int
	ranks      int
	members    int
	grace      time.Duration
	tracePath  string
	// saveEvery is the plainest checkpointing there is: save on a timer, whatever the governor says.
	// It is the job's own habit, and it is what the baseline arm of an experiment runs on — a
	// baseline that never writes compares nothing.
	saveEvery time.Duration
	// contract writes gen-N directories with a manifest sealed last (0025), instead of step-N with the
	// older shape. The fault matrix runs against this: a writer that does not seal cannot be judged.
	contract bool
	// tear corrupts the last block of the final shard while keeping the size, so the declared hashes
	// describe a file that is not on disk. That is a kill landing between two syscalls.
	tear bool
	// fsyncMS forces a Sync after every 64 KiB, so a shard pays many real fsyncs rather than one. The time
	// comes from syscalls, not from a sleep: a sleep would test the timer, not the deadline.
	fsyncMS int
}

func main() {
	var c config
	flag.StringVar(&c.dir, "dir", "", "the checkpoint directory")
	flag.IntVar(&c.steps, "steps", 300, "how many steps to run")
	flag.IntVar(&c.stepMS, "step-ms", 100, "milliseconds per step")
	flag.IntVar(&c.writeMS, "write-ms", 800, "milliseconds to write one checkpoint")
	flag.IntVar(&c.shardBytes, "shard-bytes", 1<<20, "bytes per shard")
	flag.IntVar(&c.ranks, "ranks", 2, "shards per checkpoint")
	flag.IntVar(&c.members, "members", 1, "ranks in this unit (written to .ckpt-governor/members)")
	flag.DurationVar(&c.grace, "grace", 30*time.Second, "how long to keep training after SIGTERM")
	flag.DurationVar(&c.saveEvery, "save-every", 0, "save on a timer this often (the job's own habit; 0 = never)")
	flag.StringVar(&c.tracePath, "trace", "", "write a replayable trace here")
	flag.BoolVar(&c.contract, "contract", false, "write gen-N with a manifest sealed last (0025)")
	flag.BoolVar(&c.tear, "tear", false, "corrupt the last block of the final shard, keeping its size")
	flag.IntVar(&c.fsyncMS, "fsync-ms", 0, "fsync after every 64 KiB, so a write pays many real syncs")
	flag.Parse()

	if c.dir == "" {
		fmt.Fprintln(os.Stderr, "usage: ckpttrainer --dir=<checkpointDir> [--steps=300] [--step-ms=100] [--write-ms=800] [--trace=trace.jsonl]")
		os.Exit(2)
	}
	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(c config) error {
	return runWith(c, onSignal())
}

// onSignal turns SIGTERM or ^C into a closed channel, so the loop can be tested without sending a
// signal to the test binary itself.
func onSignal() <-chan struct{} {
	done := make(chan struct{})
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		close(done)
	}()
	return done
}

func runWith(c config, stop <-chan struct{}) error {
	base := filepath.Join(c.dir, ".ckpt-governor")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	if err := writeFile(base, "boot", strconv.FormatInt(time.Now().UnixNano(), 36)); err != nil {
		return err
	}
	// Who is in this unit. The controller uses it to tell one job from a gang, and a gang that
	// shrinks is a different job.
	if err := writeFile(base, "members", memberList(c.members)); err != nil {
		return err
	}
	// What this job calls normal speed, in the unit the controller compares against. The job owns
	// the number: the controller must not decide what fast means for a workload it cannot see.
	if err := writeFile(base, "step-baseline", strconv.Itoa(c.stepMS)); err != nil {
		return err
	}
	if err := writeFile(base, "write-baseline", strconv.Itoa(c.writeMS)); err != nil {
		return err
	}
	// What the next checkpoint will weigh: the job knows its own shape, and the headroom rule needs
	// a size before the first seal exists.
	if err := writeFile(base, "expected-bytes", strconv.FormatInt(int64(c.shardBytes)*int64(c.ranks), 10)); err != nil {
		return err
	}

	tr, err := newTrace(c.tracePath)
	if err != nil {
		return err
	}
	defer tr.Close()

	// A real job is stopped by a signal, not by reaching a step count. The signal writes `stopping`
	// and then the job keeps working for as long as the grace period allows: that window is the only
	// chance to turn "we are about to lose everything" into a checkpoint.
	var stopping bool
	var graceUntil time.Time

	lastStep := time.Now()
	at := int64(0)
	lastSave := int64(-1)
	lastTimer := time.Now() // --save-every's clock
	served := ""            // the request id this job has already acted on
	var lastWriteAt = time.Now()
	var lastWriteDur time.Duration
	absentNoted := false
	for step := int64(0); ; step++ {
		select {
		case <-stop:
			if !stopping {
				stopping = true
				graceUntil = time.Now().Add(c.grace)
				if err := writeFile(base, "stopping", "1"); err != nil {
					return err
				}
			}
		default:
		}
		if stopping {
			// The job owns the deadline it was given: it reports what is left, and the controller
			// never invents a second one.
			left := int64(time.Until(graceUntil).Seconds())
			if left < 0 {
				left = 0
			}
			_ = writeFile(base, "seconds-left", strconv.FormatInt(left, 10))
		}
		if step >= int64(c.steps) {
			break
		}
		if stopping && time.Now().After(graceUntil) {
			break
		}

		if err := writeFile(base, "step", strconv.FormatInt(step, 10)); err != nil {
			return err
		}
		at = step
		tr.Event("step", step, 0, false)
		wrote := false
		if step > lastSave {
			p := readPlan(c.dir, time.Now())
			req := p.requestFor(served)
			own := p.ownTimer(time.Now(), lastWriteAt, lastWriteDur)
			// --save-every is the job's own habit, configured where the job runs. It exists so an
			// experiment can compare two jobs that both save: without a governor there is no plan to
			// learn a period from, and a baseline that never writes compares nothing.
			if c.saveEvery > 0 && time.Since(lastTimer) >= c.saveEvery {
				req, own = "", true
				served = ""
			}
			if req != "" || own {
				w0 := time.Now()
				if err := writeCheckpoint(c, base, step, tr); err != nil {
					return err
				}
				lastWriteDur = time.Since(w0)
				lastWriteAt = time.Now()
				_ = writeFile(base, "write-rate", strconv.FormatInt(lastWriteDur.Milliseconds(), 10))
				lastSave = step
				lastTimer = time.Now()
				if req != "" {
					served = req
				}
				wrote = true
			}
			// The governor is gone and the user answered "none": nothing time-based will ever write.
			// Saying so once is the difference between a signature and an accident.
			if p.readable && !p.live && req == "" && p.none && !absentNoted {
				fmt.Fprintln(os.Stderr, "governor absent; no-us=none: no writes")
				absentNoted = true
			}
		}
		// A write legitimately slows the loop down, so the step rate is only updated on the steps
		// where nothing was written; otherwise every checkpoint would look like a slow job.
		if !wrote {
			if ms := time.Since(lastStep).Milliseconds(); ms > 0 {
				_ = writeFile(base, "step-rate", strconv.FormatInt(ms, 10))
			}
		}
		// The second phase: the final write is on disk, so the job says it is done. Whether it may
		// leave is the controller's answer, not its own — a job that releases itself has no
		// protocol, only a habit, and there is nobody left to tell whether the write was worth it.
		if wrote && stopping {
			break
		}

		lastStep = time.Now()
		time.Sleep(time.Duration(c.stepMS) * time.Millisecond)
	}

	// The end of a run: training is over, or the user's grace period ran out. Whatever exists now is
	// the work that matters, so this is the one place where the checkpoint is a promise instead of a
	// configuration — and the job then *waits* for the answer instead of exiting first, because
	// exiting first is how the last state is lost. The wait is bounded by the job's own grace period:
	// a governor cannot hold a job that never answers.
	if err := writeFile(base, "ending", "1"); err != nil {
		return err
	}
	finishBy := time.Now().Add(c.grace)
	if stopping && graceUntil.Before(finishBy) {
		// A stopped job already announced its own deadline, and it must not outlive it: the answer
		// has to arrive inside the grace the platform gave, not inside a second one we invented.
		finishBy = graceUntil
	}
	for time.Now().Before(finishBy) {
		// The controller answered: there is nothing left to wait for.
		if _, err := os.Stat(filepath.Join(base, "may-exit")); err == nil {
			return nil
		}
		p := readPlan(c.dir, time.Now())
		if req := p.requestFor(served); req != "" {
			w0 := time.Now()
			if err := writeCheckpoint(c, base, at, tr); err != nil {
				return err
			}
			_ = writeFile(base, "write-rate", strconv.FormatInt(time.Since(w0).Milliseconds(), 10))
			served, lastSave = req, at
			// The job says nothing more: it keeps running, and whether it may leave is the controller's
			// answer, not its own.
			continue
		}
		if !p.live {
			// No live governor, so nobody will ever ask. The last state is still the promise, and it
			// does not depend on any configuration: the job keeps it itself, and says so.
			if lastSave < at {
				w0 := time.Now()
				if err := writeCheckpoint(c, base, at, tr); err != nil {
					return err
				}
				_ = writeFile(base, "write-rate", strconv.FormatInt(time.Since(w0).Milliseconds(), 10))
				lastSave = at
				fmt.Fprintln(os.Stderr, "governor absent: wrote the final checkpoint myself (the end of a run is a promise, not a setting)")
			}
			return nil
		}
		time.Sleep(time.Duration(c.stepMS) * time.Millisecond)
	}
	// Nobody answered within the job's own grace period. Leaving late is a decision and leaves a
	// trace; leaving silently is how a job loses its last write with no one noticing.
	if _, err := os.Stat(filepath.Join(base, "may-exit")); err == nil {
		return nil
	}
	return writeFile(base, "release-timeout", "1")
}

// planView is what the job needs from the published plan: what to do, for which request, how long the
// plan stays authoritative, and what to do when it is not (design-v1 §6).
type planView struct {
	readable bool // the file could be read at all
	live     bool // it was still authoritative when it was read
	action   string
	request  string
	baseline time.Duration // no-us: the job's own period if the governor is gone
	none     bool          // no-us=none: the user would not have written either
}

// readPlan reads the one file the governor publishes. Every failure is "not readable", which the
// caller treats as "the governor is gone": a corrupted plan that looks authoritative is worse than no
// plan at all.
func readPlan(dir string, now time.Time) planView {
	raw, err := os.ReadFile(filepath.Join(dir, ".ckpt-governor", "state.json"))
	if err != nil {
		return planView{}
	}
	var f struct {
		Action          string `json:"action"`
		RequestID       string `json:"requestId"`
		ExpiresAt       int64  `json:"expiresAt"`
		BaselineSeconds int64  `json:"baselineSeconds"`
		BaselineNone    bool   `json:"baselineNone"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return planView{}
	}
	return planView{
		readable: true,
		live:     now.Unix() <= f.ExpiresAt,
		action:   f.Action,
		request:  f.RequestID,
		baseline: time.Duration(f.BaselineSeconds) * time.Second,
		none:     f.BaselineNone,
	}
}

// requestFor is the id of a request this job has not acted on yet. Empty means "nothing to do".
//
// The id is the point: a plan says "save this request", not "save at every step until I change my
// mind". Without it the job would write every step after one plan, and every write after the first
// would be one the governor never asked for.
func (p planView) requestFor(served string) string {
	if !p.live || (p.action != "save" && p.action != "repair") || p.request == "" {
		return ""
	}
	if p.request == served {
		return ""
	}
	return p.request
}

// ownTimer is the fallback: the governor is gone and the user's own number (`no-us`) says how often
// the job writes by itself. The request goes out one write-time early so the seal lands inside the
// period, which is the same rule the window uses on the controller's side.
func (p planView) ownTimer(now, lastWriteAt time.Time, lead time.Duration) bool {
	if p.live || p.baseline <= 0 {
		return false
	}
	return now.Sub(lastWriteAt) >= p.baseline-lead
}

// writeContractCheckpoint writes one generation the way 0025 asks: shards, then a manifest, sealed by a
// rename. It is the only writer shape the fault matrix can judge — a directory with no manifest is a write
// in flight, and seven faults would all come back as "still writing".
func writeContractCheckpoint(c config, base string, step int64, tr *trace) error {
	gen := step
	dir := filepath.Join(c.dir, fmt.Sprintf("gen-%04d", gen))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// A generation being rewritten takes its manifest away first: a reader must never see a manifest that
	// lists shards which are being rewritten under it.
	_ = os.Remove(filepath.Join(dir, "manifest.json"))

	tr.Event("save_start", step, 0, false)

	shards := make([]verify.Shard, 0, c.ranks)
	perShard := time.Duration(c.writeMS) * time.Millisecond / time.Duration(c.ranks)
	for rank := 0; rank < c.ranks; rank++ {
		data := make([]byte, c.shardBytes)
		for i := range data {
			data[i] = byte(rank)
		}
		name := fmt.Sprintf("rank-%02d.bin", rank)
		path := filepath.Join(dir, name)
		from := float64(rank) / float64(c.ranks)
		to := float64(rank+1) / float64(c.ranks)
		if err := writeSlowly(path, data, perShard, base, from, to); err != nil {
			return err
		}
		if c.fsyncMS > 0 {
			if err := syncFor(path, time.Duration(c.fsyncMS)*time.Millisecond); err != nil {
				return err
			}
		}

		sum := sha256.Sum256(data)
		tail := data
		if len(tail) > verify.TailBytes {
			tail = tail[len(tail)-verify.TailBytes:]
		}
		tailSum := sha256.Sum256(tail)
		shards = append(shards, verify.Shard{
			Name: name, Size: int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]), TailSHA256: hex.EncodeToString(tailSum[:]),
		})
	}

	// The tear happens after the hashes are computed: the manifest describes the file the writer meant to
	// leave, and the disk holds something else. That is exactly what a kill between two syscalls produces.
	if c.tear && len(shards) > 0 {
		last := filepath.Join(dir, shards[len(shards)-1].Name)
		f, err := os.OpenFile(last, os.O_RDWR, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.WriteAt([]byte{0xDE, 0xAD, 0xBE, 0xEF}, shards[len(shards)-1].Size-4); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}

	m := verify.Manifest{
		Generation: gen, Step: step, SealedAt: time.Now().UTC().Truncate(time.Second),
		Writer: "ckpttrainer", Shards: shards,
	}
	raw, err := m.Marshal()
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "manifest.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "manifest.json")); err != nil {
		return err
	}

	tr.Event("save_end", step, int64(c.ranks*c.shardBytes), true)
	return writeFile(base, "progress", "0")
}

// syncFor keeps calling fsync on a real file until the time is spent. The duration comes from syscalls, not
// from a sleep: a sleep would test the timer and leave the deadline untested.
func syncFor(path string, want time.Duration) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	deadline := time.Now().Add(want)
	for time.Now().Before(deadline) {
		if err := f.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func writeCheckpoint(c config, base string, step int64, tr *trace) error {
	if c.contract {
		return writeContractCheckpoint(c, base, step, tr)
	}
	dir := filepath.Join(c.dir, "step-"+strconv.FormatInt(step, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// A repair of a step that already exists takes its manifest away first: a reader must never see
	// a manifest that lists shards which are being rewritten under it.
	_ = os.Remove(filepath.Join(dir, "manifest.json"))

	tr.Event("save_start", step, 0, false)

	shards := make([]map[string]any, 0, c.ranks)
	perShard := time.Duration(c.writeMS) * time.Millisecond / time.Duration(c.ranks)
	for rank := 0; rank < c.ranks; rank++ {
		data := make([]byte, c.shardBytes)
		for i := range data {
			data[i] = byte(rank)
		}
		sum := sha256.Sum256(data)
		path := filepath.Join(dir, "rank-"+strconv.Itoa(rank)+".bin")
		from := float64(rank) / float64(c.ranks)
		to := float64(rank+1) / float64(c.ranks)
		if err := writeSlowly(path, data, perShard, base, from, to); err != nil {
			return err
		}
		shards = append(shards, map[string]any{
			"rank": rank, "bytes": len(data), "sha256": hex.EncodeToString(sum[:]),
		})
	}
	raw, err := json.Marshal(map[string]any{"step": step, "shards": shards})
	if err != nil {
		return err
	}
	if err := writeFile(dir, "manifest.json", string(raw)); err != nil {
		return err
	}
	tr.Event("save_end", step, int64(c.ranks*c.shardBytes), true)
	return writeFile(base, "progress", "0")
}

// writeSlowly writes in chunks and reports progress: a real writer keeps changing the directory
// while it works, and the controller's "is it still moving" rule depends on that being true.
func writeSlowly(path string, data []byte, total time.Duration, base string, from, to float64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	const chunks = 8
	step := len(data) / chunks
	if step < 1 {
		step = 1
	}
	for i := 0; i < len(data); i += step {
		end := i + step
		if end > len(data) {
			end = len(data)
		}
		if _, err := f.Write(data[i:end]); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		// Progress is of the whole checkpoint, not of one shard: a reader that sees it fall back to
		// zero in the middle of a write would conclude the write restarted.
		frac := from + (to-from)*float64(end)/float64(len(data))
		_ = writeFile(base, "progress", fmt.Sprintf("%.2f", frac))
		time.Sleep(total / chunks)
	}
	return nil
}

func writeFile(dir, name, content string) error {
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

type trace struct {
	f *os.File
}

func newTrace(path string) (*trace, error) {
	if path == "" {
		return &trace{}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &trace{f: f}, nil
}

func (t *trace) Event(kind string, step int64, bytes int64, sealed bool) {
	if t.f == nil {
		return
	}
	e := map[string]any{"t": time.Now().UTC().Format(time.RFC3339Nano), "event": kind, "step": step}
	if bytes > 0 {
		e["bytes"] = bytes
	}
	if sealed {
		e["sealed"] = true
	}
	raw, _ := json.Marshal(e)
	fmt.Fprintln(t.f, string(raw))
}

func (t *trace) Close() {
	if t.f != nil {
		t.f.Close()
	}
}

// memberList is the unit's membership, one rank per line. Sorted, stable, no header: the file is
// read by a machine and diffed by a person.
func memberList(n int) string {
	if n < 1 {
		n = 1
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("rank-" + strconv.Itoa(i) + "\n")
	}
	return b.String()
}
