package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/exec"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// The job's own numbers, read from its glue files.
//
// Conditions are booleans and belong to probes. These are quantities the executor needs to answer one
// question — "can a write finish before we are killed" — and the algorithm must never see them
// (0016 R1: the clock and the world's numbers belong to the executor). Every one of them is optional:
// a job that writes none of them still works, and the answer is "unknown", never zero.
//
// A stop has two phases and both are the job's to write: `stopping` says it was told to stop, and
// `may-exit` says its final write landed. The controller reads both and never writes either: only the
// job can know that its own write is on disk, and a permission the job does not wait for is a file
// nobody reads.
const (
	// GlueExpectedBytes is the size of one checkpoint as the job knows it.
	GlueExpectedBytes = "expected-bytes"
	// GlueLastSave is how long the job's own previous save took.
	GlueLastSave = "last-save-seconds"
	// GlueSecondsLeft is the grace period the platform announced, in seconds.
	GlueSecondsLeft = "seconds-left"
	// GlueStopping means the job was told to stop, before its grace period starts.
	GlueStopping = "stopping"
	// GlueEnding means training is over: whatever exists now is the work that matters.
	GlueEnding = "ending"
	// GlueMayExit is our answer to the finish handshake: the job may go (decision 0017).
	GlueMayExit = "may-exit"
)

// The phases of a stop, as the controller can see them. A closed set of four, so the note is emitted
// once per transition rather than once per round.
const (
	exitUnknown = iota
	exitNoFinalWrite
	exitInFlight
	exitLanded
)

// Glue is what the job told us about itself this round. Zero values mean "not declared".
type Glue struct {
	ExpectedBytes   int64
	LastSaveSeconds float64
	SecondsLeft     float64
	// HasSeconds separates "the job announced zero seconds" from "the job announced nothing": only
	// the second one is a gap, and a zero window is a real announcement the gate must refuse against.
	HasSeconds bool
	Stopping   bool
	Ending     bool
	MayExit    bool
}

// readGlue reads the job's own numbers through the one parser that owns the files, so the executor
// and the CLI cannot disagree about what `.ckpt-governor/stopping` means. A missing or unreadable
// file is zero here on purpose: the zero value is the "not declared" answer, and no caller may turn
// it into a measurement.
func readGlue(dir string) Glue {
	a := adapters.ReadGlue(dir)
	g := Glue{Stopping: a.StopRequested, Ending: a.Ending, MayExit: a.MayExit}
	if a.HasExpected && a.ExpectedBytes > 0 {
		g.ExpectedBytes = a.ExpectedBytes
	}
	if a.HasLastSave && a.LastSaveSeconds > 0 {
		g.LastSaveSeconds = a.LastSaveSeconds
	}
	if a.HasSeconds {
		g.SecondsLeft, g.HasSeconds = float64(a.SecondsLeft), true
	}
	return g
}

// measureThroughput writes a bounded block into the governor's own directory, syncs it and times it.
// It is the only I/O the controller does for its own benefit: it runs at most once per process, its
// size is bounded so it cannot compete with training for long, and a failure is "unknown", never a
// fabricated number.
func measureThroughput(dir string, bytes int64) float64 {
	base := filepath.Join(dir, ".ckpt-governor")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return 0
	}
	block := make([]byte, 1<<20) // 1 MiB written repeatedly keeps the allocation bounded
	f, err := os.CreateTemp(base, ".throughput-*")
	if err != nil {
		return 0
	}
	name := f.Name()
	defer os.Remove(name)
	start := time.Now()
	written := int64(0)
	for written < bytes {
		n := int64(len(block))
		if bytes-written < n {
			n = bytes - written
		}
		if _, err := f.Write(block[:n]); err != nil {
			f.Close()
			return 0
		}
		written += n
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return 0
	}
	elapsed := time.Since(start).Seconds()
	if err := f.Close(); err != nil || elapsed <= 0 {
		return 0
	}
	return float64(written) / elapsed
}

// estimate is the best number we have for one write, and where it came from. Measured first, then what
// the job declared, then size over probed throughput. The source travels with the number because
// "estimated" and "measured" are different claims, and a status that blurs them is a lie.
func (l *Loop) estimate() (float64, string) {
	if w, ok := p90(l.ledger.WriteCosts); ok && w > 0 {
		return w, "measured"
	}
	if l.glue.LastSaveSeconds > 0 {
		return l.glue.LastSaveSeconds, "job"
	}
	if l.glue.ExpectedBytes > 0 {
		if bitrate := l.throughput(); bitrate > 0 {
			return float64(l.glue.ExpectedBytes) / bitrate, "probe"
		}
	}
	return 0, "unknown"
}

// throughput measures the storage once per process, and only if an estimate is ever needed: a job that
// declares nothing and has been measured never pays for the probe.
func (l *Loop) throughput() float64 {
	if l.cfg.Throughput != 0 {
		if l.cfg.Throughput < 0 {
			return 0
		}
		return l.cfg.Throughput
	}
	if l.cfg.DryRun {
		// The probe writes 32 MiB; a dry run writes nothing, so the number stays unknown. Unknown is
		// already a state this build can answer with (product-v1 §16, the fourth option).
		return 0
	}
	l.throughputOnce.Do(func() {
		l.throughputBytes = measureThroughput(l.cfg.Dir, 32<<20)
	})
	return l.throughputBytes
}

// size is the scale the space rule uses: the measured checkpoint first, then the job's declaration.
// The second return value says whether it is an estimate, so a status line can admit it.
func sizeOf(measured int64, g Glue) (int64, bool) {
	if measured > 0 {
		return measured, false
	}
	if g.ExpectedBytes > 0 {
		return g.ExpectedBytes, true
	}
	return 0, false
}

// releaseDoneJob answers the job's finish handshake: it writes `may-exit` once the last write has been
// answered one way or the other (it landed, it could not be made, or the step is already on disk).
// The write itself was decided earlier in the round and the gate has already recorded its verdict, so
// this function decides nothing about writing — it only answers the question the job is waiting on.
func (l *Loop) releaseDoneJob(now time.Time, st *Step) {
	if l.cfg.DryRun {
		// `may-exit` is a file the job acts on: answering it in a dry run would release a real job.
		if l.jobSaysDone() && l.ledger.Status.FinalOutcome != "" {
			st.Notes = append(st.Notes, "dry run: would answer the job's finish handshake")
		}
		return
	}
	if !l.jobSaysDone() || l.ledger.Status.FinalOutcome == "" {
		// Nothing has been asked yet: releasing now would send the job away before we asked it for the
		// checkpoint the whole handshake is about.
		return
	}
	released, why, err := exec.Release(l.cfg.Dir, l.attempts.Attempts, now)
	if err != nil {
		st.Notes = append(st.Notes, "could not release the done job: "+err.Error())
		return
	}
	if !released {
		return // a write is still in flight, and it is the one the grace period was for
	}
	st.Notes = append(st.Notes, "released the done job: "+why)
	// The glue was read at the start of this round; the answer was written after that, and the phase
	// report below must describe the round that just happened, not the one before it.
	l.glue.MayExit = true
	if strings.HasPrefix(l.ledger.Status.FinalOutcome, "asked") {
		// The wait ended the only way a wait can when nothing lands: it was the deadline.
		l.ledger.Status.FinalOutcome = "not made: the final write never sealed before its deadline"
		st.Notes = append(st.Notes, "final write NOT made: "+l.ledger.Status.FinalOutcome)
	}
}

// observeExit watches the finish handshake: the job says it is stopping or finished (`stopping`,
// `ending`), the controller asks for one last write, and it answers `may-exit` once that answer is
// settled (decision 0017 — the controller is the side that can see what is in flight). Its job here
// is to *report* the phase and the loss: "stopping, no final write yet, twelve minutes of work at
// risk" is exactly what a person needs at that moment.
//
// The note is emitted when the phase changes, not when the sentence changes: the age grows every
// round, and a log that repeats the same line with a new number in it is a log nobody reads. The
// growing age belongs to the status view, which is read on demand.
func (l *Loop) observeExit(now time.Time, st *Step) {
	if !l.glue.Stopping && !l.glue.Ending {
		l.exitPhase = exitUnknown
		return
	}
	phase, note := exitNoFinalWrite, ""
	switch {
	case l.glue.MayExit:
		phase, note = exitLanded, "finish: the final write is settled, the job may go"
	case l.attempts.Count().Running > 0:
		phase, note = exitInFlight, "finish: the job is done; its final write is in flight"
	default:
		age := humanAge(now.Sub(time.Unix(l.ledger.Status.SealAt, 0)))
		note = "finish: the job is done and no final write has landed (" + age + " of work at risk)"
	}
	if phase != l.exitPhase {
		l.exitPhase = phase
		st.Notes = append(st.Notes, note)
	}
}

// humanAge is how a status line says how much work is at risk: coarse on purpose, because a minute is
// the unit a person acts on.
func humanAge(d time.Duration) string {
	if d < time.Minute {
		return "under a minute"
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// bytesHuman is how a status line says a size.
func bytesHuman(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGiB", float64(n)/float64(int64(1)<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMiB", float64(n)/float64(int64(1)<<20))
	default:
		return strconv.FormatInt(n, 10) + "B"
	}
}

// WritePrice is what one checkpoint costs, and the evidence behind the number. A price with no
// provenance is the thing this product exists to replace, so the source travels with the seconds.
type WritePrice struct {
	Seconds    float64
	Source     string // measured, job, probe, unknown
	Bytes      int64
	Throughput float64 // bytes per second, when the number came from a probe
}

// PriceOf answers what one write costs in dir, without a loop:  runs before there is anything
// to govern, and the price is exactly what it needs.
//
// The 32 MiB probe runs only when the job declared a size: with no size there is nothing to price,
// and a read-only command that writes 32 MiB into the checkpoint directory is a surprise nobody asked
// for. `run` still probes once, because it needs the throughput for its first decision.
func PriceOf(dir string) WritePrice {
	g := readGlue(dir)
	var throughput float64
	if g.ExpectedBytes > 0 {
		throughput = measureThroughput(dir, 32<<20)
	}
	return priceFrom(g, throughput)
}

// PriceAtStart is the same question for a running loop: it reads the directory itself instead of
// waiting for a round, because the first round is the moment the user is being asked to decide.
func (l *Loop) PriceAtStart() WritePrice { return priceFrom(readGlue(l.cfg.Dir), l.throughput()) }

func priceFrom(g Glue, throughput float64) WritePrice {
	switch {
	case g.LastSaveSeconds > 0:
		return WritePrice{Seconds: g.LastSaveSeconds, Source: "job", Bytes: g.ExpectedBytes}
	case g.ExpectedBytes > 0 && throughput > 0:
		return WritePrice{Seconds: float64(g.ExpectedBytes) / throughput, Source: "probe",
			Bytes: g.ExpectedBytes, Throughput: throughput}
	}
	return WritePrice{Source: "unknown"}
}

// Describe renders a price as one clause a person can read, or says plainly that it is unknown.
func (p WritePrice) Describe() string {
	switch {
	case p.Seconds <= 0:
		return "unknown (nothing measured, and the job has not declared a size)"
	case p.Source == "probe" && p.Throughput > 0:
		return fmt.Sprintf("%.1fs (%s at %s/s)", p.Seconds, humanBytes(p.Bytes), humanBytes(int64(p.Throughput)))
	case p.Source == "job":
		return fmt.Sprintf("%.1fs (the job declared it)", p.Seconds)
	case p.Source == "measured":
		return fmt.Sprintf("%.1fs (measured)", p.Seconds)
	}
	return fmt.Sprintf("%.1fs", p.Seconds)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
