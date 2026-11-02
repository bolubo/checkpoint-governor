package observe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// writeCheckpoint writes a sealed checkpoint: one shard per entry of sizes, and a manifest that
// lists exactly those sizes.
func writeCheckpoint(t *testing.T, root string, step int64, sizes ...int64) {
	t.Helper()
	dir := filepath.Join(root, dirName(step))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shards := make([]map[string]any, 0, len(sizes))
	for rank, size := range sizes {
		name := shardName(rank)
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		shards = append(shards, map[string]any{"rank": rank, "bytes": size, "sha256": "unused"})
	}
	raw, err := json.Marshal(map[string]any{"step": step, "shards": shards})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeUnsealed writes a directory without a manifest: a write that has not finished.
func writeUnsealed(t *testing.T, root string, step int64, size int64) {
	t.Helper()
	dir := filepath.Join(root, dirName(step))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, shardName(0)), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func request(t *testing.T, dir string) Request {
	t.Helper()
	return Request{
		Dir:    dir,
		Now:    now,
		UnitID: "train/llama",
		Stat:   func(string) (int64, int64, error) { return 1_000, 10_000, nil },
	}
}

func copyAt(s *policy.Snapshot, step int64) *policy.Copy {
	for i := range s.Copies {
		if s.Copies[i].Step == step {
			return &s.Copies[i]
		}
	}
	return nil
}

func TestScanReadsASealedCheckpoint(t *testing.T) {
	dir := t.TempDir()
	writeCheckpoint(t, dir, 4, 100, 100)
	writeCheckpoint(t, dir, 8, 100, 100)

	req := request(t, dir)
	req.Seen = map[string]time.Time{
		"step-4":                 now.Add(-2 * time.Minute),
		"step-4/" + ManifestName: now.Add(-2 * time.Minute),
		"step-8":                 now.Add(-1 * time.Minute),
		"step-8/" + ManifestName: now.Add(-1 * time.Minute),
	}
	s, rep, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Copies) != 2 {
		t.Fatalf("copies = %d, want 2 (%v)", len(s.Copies), rep.Notes)
	}
	if c := copyAt(&s, 8); c == nil || c.State != policy.CopySealed || c.Bytes != 200 {
		t.Fatalf("step 8 = %+v, want sealed with 200 bytes", c)
	}
	if s.CheckpointBytes != 200 {
		t.Fatalf("checkpoint bytes = %d, want 200", s.CheckpointBytes)
	}
	// When a copy was first seen is a fact about the copy, not about the contract: the snapshot
	// carries the copies and the ledger carries the seal. A reader that wants "when did the newest
	// one land" reads the newest copy, and this is that number.
	if want := now.Add(-1 * time.Minute); !copyAt(&s, 8).CreatedAt.Equal(want) {
		t.Fatalf("newest copy first seen = %s, want %s", copyAt(&s, 8).CreatedAt, want)
	}
	if s.Step != 8 {
		t.Fatalf("step = %d, want 8 (the floor from the newest sealed copy)", s.Step)
	}
	if got := s.Evidence["step.source"].Text; got != "checkpoint" {
		t.Fatalf("step source = %q, want checkpoint", got)
	}
}

func TestScanCallsAShortShardBroken(t *testing.T) {
	dir := t.TempDir()
	writeCheckpoint(t, dir, 8, 100, 100)
	// Truncate one shard after the manifest was written: the structure no longer matches.
	if err := os.WriteFile(filepath.Join(dir, dirName(8), shardName(1)), make([]byte, 50), 0o644); err != nil {
		t.Fatal(err)
	}

	s, rep, err := Scan(request(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	c := copyAt(&s, 8)
	if c == nil || c.State == policy.CopySealed {
		t.Fatalf("step 8 = %+v, want not sealed", c)
	}
	if len(rep.Notes) == 0 {
		t.Fatal("a broken shard must be explained in the report")
	}
	if s.CheckpointBytes != 0 {
		t.Fatalf("checkpoint bytes = %d, want 0 (nothing is sealed)", s.CheckpointBytes)
	}
}

func TestScanCallsAMissingShardBroken(t *testing.T) {
	dir := t.TempDir()
	writeCheckpoint(t, dir, 8, 100, 100)
	if err := os.Remove(filepath.Join(dir, dirName(8), shardName(0))); err != nil {
		t.Fatal(err)
	}

	s, _, err := Scan(request(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if c := copyAt(&s, 8); c == nil || c.State == policy.CopySealed {
		t.Fatalf("step 8 = %+v, want not sealed", c)
	}
}

// A sealed directory whose shard changed afterwards is neither the old checkpoint nor the new one.
// Sizes can still match, so the only evidence is the order the writer left behind.
func TestScanCallsACheckpointRewrittenAfterItsSealBroken(t *testing.T) {
	dir := t.TempDir()
	writeCheckpoint(t, dir, 8, 100)

	// Age the manifest: the shard below is now newer than the seal.
	manifest := filepath.Join(dir, dirName(8), ManifestName)
	old := now.Add(-time.Hour)
	if err := os.Chtimes(manifest, old, old); err != nil {
		t.Fatal(err)
	}

	s, rep, err := Scan(request(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if c := copyAt(&s, 8); c == nil || c.State == policy.CopySealed {
		t.Fatalf("step 8 = %+v, want not sealed", c)
	}
	found := false
	for _, f := range rep.Failures {
		if f.Code == FailSealStale {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures = %+v, want seal_stale", rep.Failures)
	}
}

func TestScanCallsAStepMismatchBroken(t *testing.T) {
	dir := t.TempDir()
	writeCheckpoint(t, dir, 8, 100)
	// Rewrite the manifest with a different step: the directory and its content disagree.
	raw, _ := json.Marshal(map[string]any{"step": 9, "shards": []map[string]any{{"rank": 0, "bytes": 100}}})
	if err := os.WriteFile(filepath.Join(dir, dirName(8), ManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	s, _, err := Scan(request(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if c := copyAt(&s, 8); c == nil || c.State == policy.CopySealed {
		t.Fatalf("step 8 = %+v, want not sealed", c)
	}
}

func TestScanSeesAWriteInFlight(t *testing.T) {
	dir := t.TempDir()
	writeUnsealed(t, dir, 9, 100)

	req := request(t, dir)
	req.Seen = map[string]time.Time{"step-9": now.Add(-10 * time.Second)}
	s, rep, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	if !s.SaveInFlight {
		t.Fatal("a directory seen 10s ago without a manifest is a write in flight")
	}
	if want := now.Add(-10 * time.Second); !s.SaveStartedAt.Equal(want) {
		t.Fatalf("save started = %s, want %s", s.SaveStartedAt, want)
	}
	if rep.InFlight != "step-9" {
		t.Fatalf("report in flight = %q, want step-9", rep.InFlight)
	}
}

func TestScanDoesNotCallAStaleDirectoryInFlight(t *testing.T) {
	dir := t.TempDir()
	writeUnsealed(t, dir, 9, 100)

	req := request(t, dir)
	req.Seen = map[string]time.Time{"step-9": now.Add(-10 * time.Minute)}
	s, _, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	if s.SaveInFlight {
		t.Fatal("a directory untouched for ten minutes is not a write in flight")
	}
}

func TestScanWritesUnknownValuesWhereItCannotMeasure(t *testing.T) {
	dir := t.TempDir()
	req := request(t, dir)
	req.Stat = func(string) (int64, int64, error) { return 0, 0, os.ErrPermission }

	s, rep, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	if s.FreeBytes != -1 {
		t.Fatalf("free space = %d, want -1 (a volume that could not be read is unknown)", s.FreeBytes)
	}
	if s.SaveProgress != -1 {
		t.Fatalf("progress = %v, want -1", s.SaveProgress)
	}
	if s.CheckpointBytes != 0 {
		t.Fatalf("checkpoint bytes = %d, want 0 (decision 0002)", s.CheckpointBytes)
	}
	if len(rep.Unreadable) == 0 {
		t.Fatal("a failed volume read must be reported")
	}
}

func TestScanPrefersTheJobsOwnStep(t *testing.T) {
	dir := t.TempDir()
	writeCheckpoint(t, dir, 8, 100)

	step := int64(100)
	req := request(t, dir)
	req.Step = &step
	s, _, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	if s.Step != 100 {
		t.Fatalf("step = %d, want 100 (the job's own counter)", s.Step)
	}
	if got := s.Evidence["step.source"].Text; got != "job" {
		t.Fatalf("step source = %q, want job", got)
	}
}

func TestScanFailsWhenTheDirectoryCannotBeRead(t *testing.T) {
	_, _, err := Scan(request(t, filepath.Join(t.TempDir(), "missing")))
	if err == nil {
		t.Fatal("an unreadable directory must be an error: a snapshot without copies would say 'write one'")
	}
}

// TestScanCost prints the cost of one round, which is the number the design promises to measure.
func TestScanCost(t *testing.T) {
	dir := t.TempDir()
	for i := int64(1); i <= 10; i++ {
		writeCheckpoint(t, dir, i*10, 1000, 1000)
	}
	req := request(t, dir)
	req.Seen = map[string]time.Time{}
	for i := int64(1); i <= 10; i++ {
		name := dirName(i * 10)
		req.Seen[name] = now.Add(-time.Duration(11-i) * time.Minute)
		req.Seen[name+"/"+ManifestName] = now.Add(-time.Duration(11-i) * time.Minute)
	}

	const rounds = 200
	start := time.Now()
	for i := 0; i < rounds; i++ {
		if _, _, err := Scan(req); err != nil {
			t.Fatal(err)
		}
	}
	perRound := time.Since(start) / rounds
	t.Logf("one round over 10 copies: %s (target < 1ms)", perRound)
}

// setModTime ages a directory and everything directly inside it, which is what "quiet" is read from.
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

func TestScanCallsAQuietWriteAbandoned(t *testing.T) {
	dir := t.TempDir()
	writeUnsealed(t, dir, 9, 100)
	setModTime(t, filepath.Join(dir, dirName(9)), now.Add(-10*time.Minute))

	req := request(t, dir)
	req.Seen = map[string]time.Time{"step-9": now.Add(-10 * time.Minute)}
	s, rep, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	if c := copyAt(&s, 9); c == nil || c.State != policy.CopyBroken {
		t.Fatalf("step 9 = %+v, want broken (quiet for ten minutes)", c)
	}
	if s.SaveInFlight {
		t.Fatal("a dead write is not in flight")
	}
	if len(rep.Notes) == 0 || !strings.Contains(rep.Notes[0], "write_abandoned") {
		t.Fatalf("report = %v, want it to name write_abandoned", rep.Notes)
	}
}

func TestScanKeepsARecentWriteInFlight(t *testing.T) {
	dir := t.TempDir()
	writeUnsealed(t, dir, 9, 100)
	setModTime(t, filepath.Join(dir, dirName(9)), now.Add(-2*time.Second))

	req := request(t, dir)
	req.Seen = map[string]time.Time{"step-9": now.Add(-2 * time.Second)}
	s, _, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	if c := copyAt(&s, 9); c == nil || c.State != policy.CopyUnverified {
		t.Fatalf("step 9 = %+v, want unverified", c)
	}
	if !s.SaveInFlight {
		t.Fatal("a directory that changed two seconds ago is a write in flight")
	}
}

func TestScanScalesTheAbandonWindowWithTheWriteCost(t *testing.T) {
	dir := t.TempDir()
	writeUnsealed(t, dir, 9, 100)
	setModTime(t, filepath.Join(dir, dirName(9)), now.Add(-5*time.Minute))

	// A write that costs 200s makes the window 600s: five quiet minutes are not death yet.
	req := request(t, dir)
	req.WriteCostSamples = []float64{200}
	req.Seen = map[string]time.Time{"step-9": now.Add(-5 * time.Minute)}
	s, _, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	if c := copyAt(&s, 9); c == nil || c.State == policy.CopyBroken {
		t.Fatalf("step 9 = %+v, want not broken with a 600s window", c)
	}
}

func TestScanPrefersTheCallersChangeClock(t *testing.T) {
	dir := t.TempDir()
	writeUnsealed(t, dir, 9, 100)
	// The file system says "now"; the controller says it last saw a change five minutes ago. The
	// controller's clock wins, because it is the one every other duration is measured on.
	req := request(t, dir)
	req.Seen = map[string]time.Time{"step-9": now.Add(-5 * time.Minute)}
	req.Changed = map[string]time.Time{"step-9": now.Add(-5 * time.Minute)}
	s, _, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	if c := copyAt(&s, 9); c == nil || c.State != policy.CopyBroken {
		t.Fatalf("step 9 = %+v, want broken from the caller's clock", c)
	}
}

// The chain that matters: a dead write must reach the policy as "repair", not as "no checkpoint".
func TestADeadWriteAsksThePolicyForARepair(t *testing.T) {
	dir := t.TempDir()
	writeCheckpoint(t, dir, 4, 100)
	writeUnsealed(t, dir, 8, 100)
	setModTime(t, filepath.Join(dir, dirName(8)), now.Add(-10*time.Minute))

	req := request(t, dir)
	req.Seen = map[string]time.Time{"step-4": now.Add(-20 * time.Minute), "step-8": now.Add(-10 * time.Minute)}
	req.WriteCostSamples = []float64{30}
	s, _, err := Scan(req)
	if err != nil {
		t.Fatal(err)
	}
	d := policy.Risk{}.Decide(policy.Bounds{NoUs: policy.NoUsNone, MinCopies: 1}, s)
	if d.Action != policy.ActionRepair || !d.SaveNow {
		t.Fatalf("action = %q, saveNow = %v, want repair", d.Action, d.SaveNow)
	}
}

// The delivery sheet must cover the contract exactly: a field without a source is a field that
// silently stays zero, and an entry without a field is a stale promise.
func TestTheDeliverySheetCoversTheContract(t *testing.T) {
	typ := reflect.TypeOf(policy.Snapshot{})
	sheet := Delivery()

	missing := []string{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if _, ok := sheet[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d fields have no source: %v", len(missing), missing)
	}

	known := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		known[typ.Field(i).Name] = true
	}
	for name := range sheet {
		if !known[name] {
			t.Fatalf("the sheet names %q, which is not a field of the contract", name)
		}
	}
	t.Logf("fields: %d read, %d either produced or declared unknown", typ.NumField(), len(sheet))
}

func TestP90(t *testing.T) {
	if v, ok := p90([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}); !ok || v != 9 {
		t.Fatalf("p90 = %v, %v; want 9", v, ok)
	}
	if _, ok := p90(nil); ok {
		t.Fatal("no samples must be unknown")
	}
}

func TestParseStepName(t *testing.T) {
	cases := map[string]bool{
		"step-0": true, "step-12000": true,
		// The contract writer (0025) names its directories by generation. Not accepting these made every
		// checkpoint written under the contract invisible to this package (F11).
		"gen-1": true, "gen-0007": true,
		"step-": false, "step-x": false, "step--1": false, "other": false, "step-1x": false,
		"gen-": false, "gen-x": false, "gen--1": false,
	}
	for name, want := range cases {
		if _, ok := parseStepName(name); ok != want {
			t.Errorf("parseStepName(%q) = %v, want %v", name, ok, want)
		}
	}
}
