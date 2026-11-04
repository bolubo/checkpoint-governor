package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/resume"
)

// write puts a file (or an empty file) under dir, creating parents.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func declaration(t *testing.T, dir string, step int64, target string, state map[string]bool, requires []string) {
	t.Helper()
	f := resume.File{
		Schema:     resume.Schema,
		Generation: step,
		Step:       step,
		Format:     resume.FormatOther,
		World:      resume.World{Ranks: 1},
		State: resume.State{
			Model: state["model"], Optimizer: state["optimizer"], Scheduler: state["scheduler"],
			RNG: state["rng"], Dataloader: state["dataloader"],
		},
		Requires: requires,
		Target:   target,
	}
	if err := resume.Write(dir, f); err != nil {
		t.Fatal(err)
	}
	if target != "" && target != "." {
		// resume.Write always names the file resume.json; the artifact form is renamed beside it.
		if err := os.Rename(filepath.Join(dir, resume.FileName), filepath.Join(dir, target+".resume.json")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEveryKindIsFound(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, root string)
		kind  string
		step  int64
	}{
		{"gen", func(t *testing.T, root string) {
			write(t, filepath.Join(root, "gen-0007"), "manifest.json", "{}")
			declaration(t, filepath.Join(root, "gen-0007"), 7, ".", nil, nil)
		}, KindGen, 7},
		{"hf", func(t *testing.T, root string) {
			write(t, filepath.Join(root, "checkpoint-1000"), "trainer_state.json", "{}")
		}, KindHF, 1000},
		{"lightning", func(t *testing.T, root string) {
			write(t, root, "last.ckpt", "weights")
		}, KindLightning, 0},
		{"composer", func(t *testing.T, root string) {
			write(t, root, "ep1-rank0", "shard")
			declaration(t, root, 1, "ep1-rank0", nil, nil)
		}, KindComposer, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.build(t, root)
			res, err := Scan(root, Config{})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Found {
				t.Fatalf("%s: nothing found", tc.name)
			}
			if res.Newest.Kind != tc.kind {
				t.Fatalf("%s: kind = %s, want %s", tc.name, res.Newest.Kind, tc.kind)
			}
			t.Logf("%s: %s step %d in %s", tc.name, res.Newest.Kind, res.Newest.Step, res.Newest.Dir)
		})
	}
}

func TestOurGenerationWinsOverANewerFrameworkCopy(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "gen-0003"), "manifest.json", "{}")
	write(t, filepath.Join(root, "checkpoint-900"), "trainer_state.json", "{}")
	res, err := Scan(root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Newest.Kind != KindGen || res.Newest.Step != 3 {
		t.Fatalf("newest = %s step %d, want gen step 3", res.Newest.Kind, res.Newest.Step)
	}
	if res.Count != 2 {
		t.Fatalf("count = %d, want 2", res.Count)
	}
}

func TestTheHighestStepWinsAndATieIsExplained(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "checkpoint-1000"), "trainer_state.json", "{}")
	write(t, root, "last.ckpt", "weights")
	declaration(t, root, 4000, "last.ckpt", nil, nil)
	res, err := Scan(root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Newest.Kind != KindLightning || res.Newest.Step != 4000 {
		t.Fatalf("newest = %s step %d, want lightning step 4000", res.Newest.Kind, res.Newest.Step)
	}

	// Same step, different kinds: the fixed priority decides, and the note says so.
	root = t.TempDir()
	write(t, filepath.Join(root, "checkpoint-500"), "trainer_state.json", "{}")
	write(t, root, "epoch.ckpt", "weights")
	declaration(t, root, 500, "epoch.ckpt", nil, nil)
	res, err = Scan(root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Newest.Kind != KindHF {
		t.Fatalf("tie chose %s, want %s", res.Newest.Kind, KindHF)
	}
	if !strings.Contains(res.Note, "step 500") {
		t.Fatalf("note = %q, want it to name the tie", res.Note)
	}
}

func TestAHalfWrittenDirectoryIsNotTheNewest(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "checkpoint-1000"), "optimizer.pt", "") // bytes, no completion mark
	res, err := Scan(root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found {
		t.Fatalf("a half-written directory was counted as sealed: %+v", res.Newest)
	}
	if res.Unsealed == nil || res.Unsealed.Artifact() != "checkpoint-1000" {
		t.Fatalf("unsealed = %+v, want checkpoint-1000 named", res.Unsealed)
	}
}

func TestAnEmptyDirectoryHasNothing(t *testing.T) {
	res, err := Scan(t.TempDir(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found || res.Unsealed != nil || res.Count != 0 {
		t.Fatalf("empty directory reported %+v", res)
	}
}

func TestACustomGlobFindsAWriterWeNeverHeardOf(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "weights-250"), "model.bin", "bytes")
	cfg := Config{Glob: "weights-*", StepRegex: `weights-(\d+)`}
	declaration(t, filepath.Join(root, "weights-250"), 250, ".", nil, nil)

	res, err := Scan(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || res.Newest.Kind != KindCustom || res.Newest.Step != 250 {
		t.Fatalf("custom layout = %+v, want custom step 250", res.Newest)
	}
}

func TestTheProbeTurnsAnOpaqueCopyIntoADeclaration(t *testing.T) {
	root := t.TempDir()
	artifact := write(t, root, "x.ckpt", "bytes")

	script := filepath.Join(root, "probe.sh")
	write(t, root, "probe.sh", "#!/bin/sh\nprintf '%s' '{\"state\":{\"model\":true,\"optimizer\":true,\"scheduler\":true,\"rng\":true},\"requires\":[\"model\",\"optimizer\",\"scheduler\",\"rng\"]}'\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ProbeCmd: script + " {dir}"}
	c := Copy{Dir: root, Target: "x.ckpt", Step: 42, Kind: KindLightning, Sealed: true}

	f, declared, probed, err := c.Declaration(cfg)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if declared || !probed {
		t.Fatalf("declared=%v probed=%v, want the probe to answer", declared, probed)
	}
	if !f.State.Optimizer || len(f.Requires) != 4 || f.Step != 42 {
		t.Fatalf("probe declaration = %+v, want four requires at step 42", f)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatal(err)
	}
}

func TestAFailingProbeIsUnknownNotBad(t *testing.T) {
	root := t.TempDir()
	write(t, root, "x.ckpt", "bytes")
	c := Copy{Dir: root, Target: "x.ckpt", Step: 1, Kind: KindLightning, Sealed: true}

	_, declared, _, err := c.Declaration(Config{ProbeCmd: "/bin/false {dir}"})
	if err != nil {
		// a failed probe is reported, never turned into "not resumable"
		if !strings.Contains(err.Error(), "probe_cmd failed") {
			t.Fatalf("error = %v, want it to name probe_cmd failed", err)
		}
	}
	if declared {
		t.Fatal("a failing probe must not produce a declaration")
	}
}

// A probe that never answers is a refusal with a name, not a hang: the restore has five seconds and
// then it says so.
func TestAProbeThatNeverAnswersNamesTheTimeout(t *testing.T) {
	old := ProbeTimeout
	ProbeTimeout = 100 * time.Millisecond
	defer func() { ProbeTimeout = old }()

	root := t.TempDir()
	write(t, root, "x.ckpt", "bytes")
	c := Copy{Dir: root, Target: "x.ckpt", Step: 1, Kind: KindLightning, Sealed: true}

	_, declared, probed, err := c.Declaration(Config{ProbeCmd: "/bin/sleep 2"})
	if err != nil {
		if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "probe_cmd failed") {
			t.Fatalf("error = %v, want probe_cmd failed: timed out", err)
		}
	}
	if declared || !probed {
		t.Fatalf("declared=%v probed=%v, want a failed probe", declared, probed)
	}
}

// A multi-rank copy that does not hold every rank's shard cannot be resumed, whatever its
// declaration says. The check only fires when the manifest marks ranks: a writer that never recorded
// membership left nothing to check, and inventing a missing rank would be a guess.
func TestAMissingRankShardIsNotResumable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "gen-0007")
	write(t, dir, "manifest.json", `{"generation":7,"step":7,"sealedAt":"2026-11-02T04:55:44Z","writer":"x","shards":[`+
		`{"name":"rank0-model.bin","size":1,"sha256":"`+strings.Repeat("a", 64)+`","member":0},`+
		`{"name":"rank2-model.bin","size":1,"sha256":"`+strings.Repeat("b", 64)+`","member":2}]}`)
	if err := resume.Write(dir, resume.File{
		Schema: resume.Schema, Generation: 7, Step: 7, Format: resume.FormatDCP,
		World: resume.World{Ranks: 3},
		State: resume.State{Model: true, Optimizer: true, Scheduler: true, RNG: true, Dataloader: true},
	}); err != nil {
		t.Fatal(err)
	}

	c := Copy{Dir: dir, Target: ".", Step: 7, Kind: KindGen, Sealed: true, Declared: true}
	v := Judge(c, Config{}, resume.World{})
	if v.Line() != "not resumable: rank 1 shard missing" {
		t.Fatalf("verdict = %q, want the missing rank named", v.Line())
	}

	// A manifest without member marks is not checked: nothing was recorded, so nothing is assumed.
	write(t, dir, "manifest.json", `{"generation":7,"step":7,"sealedAt":"2026-11-02T04:55:44Z","writer":"x","shards":[`+
		`{"name":"rank0-model.bin","size":1,"sha256":"`+strings.Repeat("a", 64)+`"}]}`)
	if v := Judge(c, Config{}, resume.World{}); !v.Resumable() {
		t.Fatalf("an unmarked manifest was refused: %s", v.Line())
	}
}

// A single-rank copy is never asked for other ranks' shards.
func TestASingleRankCopySkipsTheShardCheck(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "gen-0007")
	write(t, dir, "manifest.json", `{"generation":7,"step":7,"sealedAt":"2026-11-02T04:55:44Z","writer":"x","shards":[`+
		`{"name":"rank0-model.bin","size":1,"sha256":"`+strings.Repeat("a", 64)+`","member":0}]}`)
	declaration(t, dir, 7, ".", map[string]bool{"model": true}, []string{"model"})
	if v := Judge(Copy{Dir: dir, Target: ".", Step: 7, Kind: KindGen, Sealed: true}, Config{}, resume.World{}); !v.Resumable() {
		t.Fatalf("single-rank copy refused: %s", v.Line())
	}
}
