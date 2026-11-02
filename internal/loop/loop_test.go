package loop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func bounds() policy.Bounds {
	return policy.Bounds{MinCopies: 1, NoUs: policy.NoUsNone}
}

func open(t *testing.T, dir string, now *time.Time) *Loop {
	t.Helper()
	l, err := Open(Config{
		Dir:    dir,
		UnitID: "train/llama",
		Bounds: bounds(),
		Policy: policy.Risk{},
		Now:    func() time.Time { return *now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// openWith is open() plus the conditions the test wants to watch.
func openWith(t *testing.T, dir string, now *time.Time, probes ...produce.Probe) *Loop {
	t.Helper()
	l, err := Open(Config{
		Dir:    dir,
		UnitID: "train/llama",
		Bounds: bounds(),
		Policy: policy.Risk{},
		Now:    func() time.Time { return *now },
		Probes: probes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func writeSealed(t *testing.T, dir string, step int64, sizes ...int64) {
	t.Helper()
	d := filepath.Join(dir, dirName(step))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	shards := make([]map[string]any, 0, len(sizes))
	for rank, size := range sizes {
		if err := os.WriteFile(filepath.Join(d, "rank-"+itoa(rank)+".bin"), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		shards = append(shards, map[string]any{"rank": rank, "bytes": size})
	}
	raw, _ := json.Marshal(map[string]any{"step": step, "shards": shards})
	if err := os.WriteFile(filepath.Join(d, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeUnsealed(t *testing.T, dir string, step int64, size int64) {
	t.Helper()
	d := filepath.Join(dir, dirName(step))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "rank-0.bin"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setModTime ages a directory tree onto the test clock. The tests run on a fixed date while the
// file system stamps real time, and any rule that compares the two has to be given the same clock.
func setModTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(path, e.Name()), at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

// Under the risk core a job with nothing to report is not written on its first round: the write has
// to come from a condition or from the user's window, and the timer belongs to the executor.
// The first round still publishes a plan, because the job has to know the controller is watching.
func TestTheFirstRoundIsCalmWithoutConditions(t *testing.T) {
	now := t0
	l := open(t, t.TempDir(), &now)

	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if st.Action != policy.ActionSkip || st.Reason != policy.ReasonCalm {
		t.Fatalf("action = %q/%q, want skip/calm", st.Action, st.Reason)
	}
	if !st.Published {
		t.Fatal("the first plan must be published")
	}
	if st.Phase != advise.PhaseIdle {
		t.Fatalf("phase = %q, want idle", st.Phase)
	}
	if l.Ledger().Status.AdviceAction != "skip" {
		t.Fatalf("advice action = %q, want skip", l.Ledger().Status.AdviceAction)
	}
}

func TestTheSamePlanIsNotRewritten(t *testing.T) {
	now := t0
	l := open(t, t.TempDir(), &now)
	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}
	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if st.Published {
		t.Fatal("an unchanged plan must not rewrite the file")
	}
}

func TestASealIsRecordedOnce(t *testing.T) {
	dir := t.TempDir()
	writeSealed(t, dir, 4, 100, 100)
	now := t0
	l := open(t, dir, &now)

	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if l.Ledger().LastSealStep != 4 {
		t.Fatalf("last seal step = %d, want 4", l.Ledger().LastSealStep)
	}
	if len(l.Ledger().Status.Writes) != 1 {
		t.Fatalf("history = %+v, want one sealed write", l.Ledger().Status.Writes)
	}
	if st.Phase != advise.PhaseSealed {
		t.Fatalf("phase = %q (action=%q reason=%q), want sealed", st.Phase, st.Action, st.Reason)
	}

	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}
	if len(l.Ledger().Status.Writes) != 1 {
		t.Fatalf("history = %+v after a second round, want still one", l.Ledger().Status.Writes)
	}
}

func TestABrokenShardAsksForARepairAndCountsOneFailure(t *testing.T) {
	dir := t.TempDir()
	writeSealed(t, dir, 4, 100)
	writeSealed(t, dir, 8, 100)
	// Break step 8 after the manifest was written.
	if err := os.WriteFile(filepath.Join(dir, dirName(8), "rank-0.bin"), make([]byte, 50), 0o644); err != nil {
		t.Fatal(err)
	}
	now := t0
	l := open(t, dir, &now)

	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if st.Action != policy.ActionRepair {
		t.Fatalf("action = %q, want repair", st.Action)
	}
	if st.Failures != 1 {
		t.Fatalf("failures = %d, want 1", st.Failures)
	}
	if st.Phase != advise.PhaseBroken {
		t.Fatalf("phase = %q, want broken", st.Phase)
	}

	// The same broken copy must not count again on the next round.
	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}
	if l.Ledger().Failures != 1 {
		t.Fatalf("failures = %d after a second round, want 1", l.Ledger().Failures)
	}
}

func TestTwoFailuresBlockTheUnit(t *testing.T) {
	dir := t.TempDir()
	writeSealed(t, dir, 4, 100)
	writeSealed(t, dir, 8, 100)
	if err := os.WriteFile(filepath.Join(dir, dirName(8), "rank-0.bin"), make([]byte, 50), 0o644); err != nil {
		t.Fatal(err)
	}
	now := t0
	l := open(t, dir, &now)

	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}

	// The repair attempt fails too: a new broken directory, at a new step.
	if err := os.RemoveAll(filepath.Join(dir, dirName(8))); err != nil {
		t.Fatal(err)
	}
	writeSealed(t, dir, 12, 100)
	if err := os.WriteFile(filepath.Join(dir, dirName(12), "rank-0.bin"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if st.Failures != 2 || st.Phase != advise.PhaseBlocked {
		t.Fatalf("failures = %d, phase = %q, want 2 and blocked", st.Failures, st.Phase)
	}
	if st.Action != policy.ActionInfeasible {
		t.Fatalf("action = %q, want infeasible (a person has to look)", st.Action)
	}
}

func TestOldCopiesAreDeletedAndTheNewestAreKept(t *testing.T) {
	dir := t.TempDir()
	for _, step := range []int64{4, 8, 12, 16} {
		writeSealed(t, dir, step, 100)
		setModTime(t, filepath.Join(dir, dirName(step)), t0.Add(-time.Hour))
	}
	now := t0
	l, err := Open(Config{
		Dir: dir, UnitID: "train/llama",
		Bounds: policy.Bounds{MinCopies: 1, NoUs: policy.NoUsNone},
		Policy: policy.Risk{},
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Deleted) == 0 {
		t.Fatalf("nothing was deleted; decision action=%q reason=%q", st.Action, st.Reason)
	}
	if _, err := os.Stat(filepath.Join(dir, dirName(16))); err != nil {
		t.Fatalf("the newest copy was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, dirName(4))); !os.IsNotExist(err) {
		t.Fatal("step 4 should have been deleted")
	}
}

func TestAnAdviceIsSeenWhenTheDirectoryAppears(t *testing.T) {
	dir := t.TempDir()
	now := t0
	// "Was it seen" only means something for advice that asks for a write, so the round needs a
	// condition that fires: a job ignoring a save request is the case this measures.
	l := openWith(t, dir, &now, &fakeProbe{name: "risk.shutdown", every: time.Second, state: produce.Fresh, value: true})
	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}
	if l.Ledger().Status.AdviceAt == 0 {
		t.Fatal("the plan was not recorded")
	}

	now = now.Add(5 * time.Second)
	writeUnsealed(t, dir, 9, 100)
	setModTime(t, filepath.Join(dir, dirName(9)), now)
	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}
	if l.Ledger().Status.SeenAt == 0 {
		t.Fatal("the write that appeared after the advice is how 'seen' is measured")
	}
	if l.Ledger().Status.Phase != advise.PhaseWriting {
		t.Fatalf("phase = %q, want writing", l.Ledger().Status.Phase)
	}
}

func TestAReopenedLoopIncrementsTheEpochAndKeepsTheHistory(t *testing.T) {
	dir := t.TempDir()
	writeSealed(t, dir, 4, 100)
	now := t0
	first := open(t, dir, &now)
	if _, err := first.Round(); err != nil {
		t.Fatal(err)
	}
	epochAfterFirst := first.Ledger().Epoch

	second := open(t, dir, &now)
	if second.Ledger().Epoch != epochAfterFirst+1 {
		t.Fatalf("epoch = %d, want %d", second.Ledger().Epoch, epochAfterFirst+1)
	}
	if len(second.Ledger().Status.Writes) != 1 {
		t.Fatalf("history = %+v, want it to survive the restart", second.Ledger().Status.Writes)
	}
}

func TestDeleteNeverRemovesTheInFlightOrTheNewest(t *testing.T) {
	dir := t.TempDir()
	writeSealed(t, dir, 4, 100)
	writeSealed(t, dir, 8, 100)
	writeUnsealed(t, dir, 12, 100)

	copies := []policy.Copy{
		{Step: 4, Bytes: 100, State: policy.CopySealed},
		{Step: 8, Bytes: 100, State: policy.CopySealed},
		{Step: 12, Bytes: 100, State: policy.CopyUnverified},
	}
	deleted, err := retireBelow(dir, 8, 1, copies, t0) // oldest=8, keep=1
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0].Step != 4 {
		t.Fatalf("deleted = %+v, want only step 4", deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, dirName(12))); err != nil {
		t.Fatal("the in-flight directory must survive")
	}
	if _, err := os.Stat(filepath.Join(dir, dirName(8))); err != nil {
		t.Fatal("the newest copy must survive")
	}
}

// writeSealedDigest writes a sealed checkpoint whose manifest carries the real digest, so that the
// verifier has something to compare against.
func writeSealedDigest(t *testing.T, dir string, step int64, data []byte) string {
	t.Helper()
	d := filepath.Join(dir, dirName(step))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	shard := filepath.Join(d, "rank-0.bin")
	if err := os.WriteFile(shard, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	raw, _ := json.Marshal(map[string]any{
		"step":   step,
		"shards": []map[string]any{{"rank": 0, "bytes": len(data), "sha256": hex.EncodeToString(sum[:])}},
	})
	if err := os.WriteFile(filepath.Join(d, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return shard
}

// writeManifest seals a step that was already being written: the shard exists, the manifest arrives.
func writeManifest(t *testing.T, dir string, step int64, size int64) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"step":   step,
		"shards": []map[string]any{{"rank": 0, "bytes": size}},
	})
	if err := os.WriteFile(filepath.Join(dir, dirName(step), "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The write cost is a bracket, not a point: the write finished somewhere between the last sighting
// that saw no manifest and the first one that did. The upper end is what the plan uses.
func TestTheWriteCostIsBracketedByTwoSightings(t *testing.T) {
	dir := t.TempDir()
	now := t0
	l := open(t, dir, &now)
	if _, err := l.Round(); err != nil { // empty directory: ask for a checkpoint
		t.Fatal(err)
	}

	// The job starts writing at t0+10s; the controller first sees the directory at +10.2s.
	now = t0.Add(10200 * time.Millisecond)
	writeUnsealed(t, dir, 0, 100)
	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}

	// Two more sightings with no manifest: the last of them is the lower end of the bracket.
	now = t0.Add(12 * time.Second)
	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(14800 * time.Millisecond)
	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}

	// The manifest appears; the write really took 5s (10.0 → 15.0), and we see it at 15.2.
	now = t0.Add(15200 * time.Millisecond)
	writeManifest(t, dir, 0, 100)
	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}

	if st.CostHi != 5 {
		t.Fatalf("upper bound = %.1fs, want 5.0s (first sighting to first seal)", st.CostHi)
	}
	if st.CostLo != 4.6 {
		t.Fatalf("lower bound = %.1fs, want 4.6s (last sighting without a manifest)", st.CostLo)
	}
	if len(l.Ledger().WriteCosts) == 0 || l.Ledger().WriteCosts[0] != 5 {
		t.Fatalf("samples = %v, want the upper bound 5.0", l.Ledger().WriteCosts)
	}
}

// The overhead counters are the basis of every later optimisation, so they are asserted against a
// fixture whose system calls can be counted by hand.
func TestOverheadCountsWhereTheWorkHappens(t *testing.T) {
	dir := t.TempDir()
	for i := int64(1); i <= 10; i++ {
		writeSealed(t, dir, i*10, 100) // one shard each
	}
	now := t0
	l, err := Open(Config{
		Dir: dir, UnitID: "train/llama", Bounds: bounds(),
		Policy: policy.Risk{}, Now: func() time.Time { return now },
		VerifyEvery: -1, // verification is measured separately
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}

	if st.Overhead.ManifestReads != 10 {
		t.Fatalf("manifest reads = %d, want 10 (one per copy)", st.Overhead.ManifestReads)
	}
	// One ReadDir, then per copy one manifest fstat and one stat per shard: 1 + 10*(1+1) = 21.
	if st.Overhead.StatCalls != 21 {
		t.Fatalf("stat calls = %d, want 21", st.Overhead.StatCalls)
	}
	if st.Overhead.Fsyncs != 2 {
		t.Fatalf("fsyncs = %d, want 2 (the first plan is published)", st.Overhead.Fsyncs)
	}
	if st.Overhead.Rounds != 1 || st.Overhead.Decisions != 1 {
		t.Fatalf("rounds/decisions = %d/%d, want 1/1", st.Overhead.Rounds, st.Overhead.Decisions)
	}
	if st.Overhead.ScanNS <= 0 {
		t.Fatal("the scan time was not measured")
	}
	if l.Ledger().Status.Overhead.ManifestReads != 10 {
		t.Fatalf("the cumulative overhead was not recorded: %+v", l.Ledger().Status.Overhead)
	}
}

// Reading a checkpoint costs more than writing it, so the read-back is rationed. A checkpoint larger
// than the remaining budget waits, and the waiting is counted.
// Removal is two steps: a rename (microseconds) and a sweep (bounded). The copy stops being a
// checkpoint at the rename, which is what the next scan must see.
func TestRetiredCopiesWaitInTheTrashAndAreSweptLater(t *testing.T) {
	dir := t.TempDir()
	writeSealed(t, dir, 4, 100)
	writeSealed(t, dir, 8, 100)

	copies := []policy.Copy{
		{Step: 4, Bytes: 100, State: policy.CopySealed},
		{Step: 8, Bytes: 100, State: policy.CopySealed},
	}
	deleted, err := retireBelow(dir, 8, 1, copies, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0].Step != 4 {
		t.Fatalf("retired = %+v, want only step 4", deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "step-4")); !os.IsNotExist(err) {
		t.Fatal("a retired copy must stop being a checkpoint immediately")
	}
	entries, err := os.ReadDir(filepath.Join(dir, trashDir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("trash = %v, %v; want one retired copy", entries, err)
	}

	// A sweep with no budget does nothing, which is what keeps a round short.
	if n, _, err := sweepTrash(dir, 0); err != nil || n != 0 {
		t.Fatalf("sweep with no budget = %d, %v; want 0", n, err)
	}
	if n, _, err := sweepTrash(dir, 1<<30); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want 1", n, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, trashDir)); len(entries) != 0 {
		t.Fatalf("trash still holds %d entries", len(entries))
	}
}

func TestTheVerifyBudgetDefersALargeCheckpoint(t *testing.T) {
	dir := t.TempDir()
	writeSealedDigest(t, dir, 4, bytes.Repeat([]byte{0xAB}, 4096))

	now := t0
	l, err := Open(Config{
		Dir: dir, UnitID: "train/llama", Bounds: bounds(),
		Policy: policy.Risk{}, Now: func() time.Time { return now },
		VerifyEvery:        time.Nanosecond,
		VerifyBytesPerHour: 1000, // smaller than the checkpoint
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if !st.VerifyDeferred {
		t.Fatal("a 4KB checkpoint under a 1KB budget must be deferred")
	}
	if st.Overhead.VerifyBytes != 0 {
		t.Fatalf("verify bytes = %d, want 0", st.Overhead.VerifyBytes)
	}
	if l.Ledger().Status.VerifyDeferred != 1 {
		t.Fatalf("deferred count = %d, want 1", l.Ledger().Status.VerifyDeferred)
	}
}

// flapping alternates its action on every call: the worst case a publisher can be handed.
type flapping struct{ n int }

func (f *flapping) Name() string { return "flapping" }
func (f *flapping) Decide(b policy.Bounds, s policy.Snapshot) policy.Decision {
	f.n++
	action := policy.ActionSkip
	if f.n%2 == 0 {
		action = policy.ActionSave
	}
	return policy.Decision{
		UnitID: s.UnitID, Generation: s.Generation, Action: action, Reason: policy.ReasonSchedule,
		Version: "flapping",
	}
}

// One writer per directory: a second controller runs, observes and reports, but publishes nothing.
func TestASecondControllerIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	now := t0
	first, err := Open(Config{
		Dir: dir, UnitID: "u", Bounds: bounds(), Policy: policy.Risk{},
		Now: func() time.Time { return now }, Holder: "a", LeaseTTL: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(Config{
		Dir: dir, UnitID: "u", Bounds: bounds(), Policy: policy.Risk{},
		Now: func() time.Time { return now }, Holder: "b", LeaseTTL: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	st1, err := first.Round()
	if err != nil {
		t.Fatal(err)
	}
	if !st1.Published {
		t.Fatal("the lease holder must publish")
	}
	st2, err := second.Round()
	if err != nil {
		t.Fatal(err)
	}
	if !st2.ReadOnly || st2.Published {
		t.Fatalf("second = %+v, want read-only and silent", st2)
	}
}

// After the lease lapses the waiting controller takes over on its own.
func TestTheLeaseLapsesAndTheSecondTakesOver(t *testing.T) {
	dir := t.TempDir()
	now := t0
	open := func(holder string) *Loop {
		l, err := Open(Config{
			Dir: dir, UnitID: "u", Bounds: bounds(), Policy: policy.Risk{},
			Now: func() time.Time { return now }, Holder: holder, LeaseTTL: 15 * time.Second,
			VerifyEvery: -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	a, b := open("a"), open("b")
	if _, err := a.Round(); err != nil {
		t.Fatal(err)
	}
	if st, _ := b.Round(); !st.ReadOnly {
		t.Fatal("b must not write while a holds the lease")
	}

	now = t0.Add(16 * time.Second)
	st, err := b.Round()
	if err != nil {
		t.Fatal(err)
	}
	// Taking over means b is now the writer: it decides. Published may be false because the plan it
	// produced is byte-identical to the one a left behind, and the publisher skips identical writes.
	if st.ReadOnly || !st.Decided {
		t.Fatalf("after expiry = %+v, want b to take over", st)
	}
}

// A controller that shuts down on purpose hands over immediately instead of making the next one
// wait out the ttl.
func TestClosingHandsTheLeaseOver(t *testing.T) {
	dir := t.TempDir()
	now := t0
	a, err := Open(Config{
		Dir: dir, UnitID: "u", Bounds: bounds(), Policy: policy.Risk{},
		Now: func() time.Time { return now }, Holder: "a", LeaseTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Round(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := Open(Config{
		Dir: dir, UnitID: "u", Bounds: bounds(), Policy: policy.Risk{},
		Now: func() time.Time { return now }, Holder: "b", LeaseTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := b.Round()
	if err != nil {
		t.Fatal(err)
	}
	if st.ReadOnly {
		t.Fatal("the lease should have been released")
	}
}

func TestAFlappingPlanIsCapped(t *testing.T) {
	dir := t.TempDir()
	now := t0
	l, err := Open(Config{
		Dir: dir, UnitID: "train/llama", Bounds: bounds(),
		Policy: &flapping{}, Now: func() time.Time { return now },
		MaxPublishPerMinute: 3, VerifyEvery: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, asked := 0, 0
	lastAction, lastReason := policy.Action(""), policy.Reason("")
	for i := 0; i < 20; i++ {
		before, _ := advise.ReadPublished(dir)
		st, err := l.Round()
		if err != nil {
			t.Fatal(err)
		}
		after, _ := advise.ReadPublished(dir)
		// A round in which the plan's *contents* changed is a publish the cap is about. A renewal —
		// the same plan with a new stamp — is the heartbeat, and it is deliberately not capped: a
		// plan that expires while we are alive tells a healthy job that we are gone.
		if after.Decision.RequestID != before.Decision.RequestID ||
			after.Decision.Action != before.Decision.Action ||
			after.Decision.Reason != before.Decision.Reason {
			published++
		}
		// A round in which the policy asked for something other than the last one was a change the
		// cap may have suppressed. Counting the asks is how the test knows the cap was exercised
		// without asking the loop to confess: more asks than publishes is the suppression.
		if st.Action != lastAction || st.Reason != lastReason {
			asked++
			lastAction, lastReason = st.Action, st.Reason
		}
		now = now.Add(time.Second)
	}
	if published > 3 {
		t.Fatalf("the plan changed %d times in a minute, want at most 3", published)
	}
	if n := len(l.ledger.PublishTimes); n > 3 {
		t.Fatalf("the breaker was charged %d times in a minute, want at most 3", n)
	}
	// The cap was exercised: the policy asked for more changes than the file was allowed to show.
	if asked <= published {
		t.Fatalf("nothing was suppressed: %d changes asked for, %d published", asked, published)
	}
	fmt.Printf("flap: %d changes published, %d rounds asked for a change, 0 expired plans\n", published, asked)
}

// A manifest is written once at the seal, so a second round over unchanged copies must not read it
// again. The stat still happens: the stale-seal rule needs the times.
func TestTheManifestCacheStopsReadingUnchangedCopies(t *testing.T) {
	dir := t.TempDir()
	for i := int64(1); i <= 3; i++ {
		writeSealed(t, dir, i*10, 100)
	}
	now := t0
	l := open(t, dir, &now)

	first, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if first.Overhead.ManifestReads != 3 {
		t.Fatalf("reads = %d, want 3 (one per copy)", first.Overhead.ManifestReads)
	}

	now = now.Add(time.Second)
	second, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if second.Overhead.ManifestReads != 0 {
		t.Fatalf("reads = %d on an unchanged round, want 0", second.Overhead.ManifestReads)
	}
	if second.Overhead.StatCalls == 0 {
		t.Fatal("the stat still has to happen: the stale-seal rule reads the times")
	}
}

// A crash between publishing the plan and recording it must not lose the fact that the job was
// already asked, and must not leave the ledger thinking it is a different epoch.
func TestACrashAfterPublishIsReconciledOnRestart(t *testing.T) {
	dir := t.TempDir()
	now := t0
	cut := errors.New("power cut")

	l, err := Open(Config{
		Dir: dir, UnitID: "train/llama", Bounds: bounds(),
		Policy: policy.Risk{}, Now: func() time.Time { return now },
		Probes: []produce.Probe{&fakeProbe{name: "risk.shutdown", every: time.Second, state: produce.Fresh, value: true}},
		Fault:  faulty("publish", cut),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Round(); !errors.Is(err, cut) {
		t.Fatalf("round error = %v, want the injected crash", err)
	}
	if _, err := os.Stat(filepath.Join(dir, advise.BaseDir, "state.json")); err != nil {
		t.Fatal("the plan must have reached the disk before the crash")
	}
	if _, err := os.Stat(filepath.Join(dir, advise.BaseDir, "internal.json")); !os.IsNotExist(err) {
		t.Fatal("the ledger should not exist: the crash happened before it was written")
	}

	back := open(t, dir, &now)
	if back.Ledger().Reconciled != 1 {
		t.Fatalf("reconciled = %d, want 1", back.Ledger().Reconciled)
	}
	if back.Ledger().Status.AdviceAt == 0 || back.Ledger().Status.AdviceAction != "save" {
		t.Fatalf("the published advice was not adopted: %+v", back.Ledger().Status)
	}
	if back.Ledger().Epoch < 2 {
		t.Fatalf("epoch = %d, want at least 2 (the adopted one plus this start)", back.Ledger().Epoch)
	}
}

// The invariant behind the reconciliation: repeated crashes never lose a seal and never count one
// twice.
func TestRepeatedCrashesDoNotLoseOrDoubleCount(t *testing.T) {
	dir := t.TempDir()
	writeSealed(t, dir, 4, 100)
	now := t0

	for i := 0; i < 10; i++ {
		l, err := Open(Config{
			Dir: dir, UnitID: "train/llama", Bounds: bounds(),
			Policy: policy.Risk{}, Now: func() time.Time { return now },
			Fault: faulty("publish", errors.New("cut")),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = l.Round() // the injected error is the point
	}

	now = t0.Add(time.Minute)
	l := open(t, dir, &now)
	for i := 0; i < 3; i++ {
		if _, err := l.Round(); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(l.Ledger().Status.Writes); got != 1 {
		t.Fatalf("history = %d entries, want exactly one seal: %+v", got, l.Ledger().Status.Writes)
	}
	if l.Ledger().Failures != 0 {
		t.Fatalf("failures = %d, want 0: a crash is not a write failure", l.Ledger().Failures)
	}
	if l.Ledger().Reconciled < 1 {
		t.Fatal("no reconciliation was recorded")
	}
}

func TestTheVerifierTurnsAMismatchIntoARepair(t *testing.T) {
	dir := t.TempDir()
	data := bytes.Repeat([]byte{0xAB}, 4096)
	shard := writeSealedDigest(t, dir, 4, data)

	// One byte changes after the seal: the structure still matches, only the content does not.
	broken := append([]byte(nil), data...)
	broken[7] = 0x00
	if err := os.WriteFile(shard, broken, 0o644); err != nil {
		t.Fatal(err)
	}

	now := t0
	l, err := Open(Config{
		Dir: dir, UnitID: "train/llama", Bounds: bounds(),
		Policy: policy.Risk{}, Now: func() time.Time { return now },
		VerifyEvery: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := l.Round(); err != nil { // the verifier runs at the end of the round
		t.Fatal(err)
	}
	if l.Ledger().Mismatch[4] == "" {
		t.Fatal("the verifier did not record the mismatch")
	}

	now = now.Add(time.Second)
	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if st.Action != policy.ActionRepair {
		t.Fatalf("action = %q, want repair after a content mismatch", st.Action)
	}
	if st.Failures != 1 {
		t.Fatalf("failures = %d, want 1", st.Failures)
	}
}

// The case the demo produced: a broken write at a higher step is the "newest" copy, so keeping one
// copy keeps the broken one and deletes the only good checkpoint.
func TestDeletionNeverRemovesTheLastSealedCopy(t *testing.T) {
	dir := t.TempDir()
	writeSealed(t, dir, 4, 100)
	writeSealed(t, dir, 8, 100)

	copies := []policy.Copy{
		{Step: 4, Bytes: 100, State: policy.CopySealed},
		{Step: 8, Bytes: 100, State: policy.CopyBroken},
	}
	deleted, err := retireBelow(dir, 4, 1, copies, t0) // oldest=4, keep=1: step 8 is "kept", step 4 is eligible
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted = %+v, want nothing: step 4 is the last sealed copy", deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, dirName(4))); err != nil {
		t.Fatal("the last sealed copy was removed")
	}
}
