package resume_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/resume"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// glue writes a job-owned file. The adoption verdict is computed from these and from manifests only, so
// a test can build the four situations a week of production produces without a training run.
func glue(t *testing.T, root, name, value string) {
	t.Helper()
	dir := filepath.Join(root, resume.GlueDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seal writes one generation the way a writer does: shard, then manifest, renamed last. The shard is one
// byte, because no question on this page reads it.
func seal(t *testing.T, root string, generation, step int64, at time.Time) {
	t.Helper()
	dir := filepath.Join(root, "gen-"+strconv.FormatInt(generation, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte{byte(generation)}
	if err := os.WriteFile(filepath.Join(dir, "rank-00.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	m := verify.Manifest{
		Generation: generation, Step: step, SealedAt: at.UTC().Truncate(time.Second),
		Writer: "test",
		Shards: []verify.Shard{{
			Name: "rank-00.bin", Size: int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]), TailSHA256: hex.EncodeToString(sum[:]),
		}},
	}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".manifest.tmp"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, ".manifest.tmp"), filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

// boot writes the job's start stamp in the same encoding the trainer uses.
func boot(t *testing.T, root string, at time.Time) {
	t.Helper()
	glue(t, root, resume.GlueBoot, strconv.FormatInt(at.UnixNano(), 36))
}

// The four sentences, plus the two inconsistencies that must not be mistaken for an adoption.
func TestAdoptionFourQuadrants(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	earlier := now.Add(-2 * time.Hour)

	t.Run("adopted", func(t *testing.T) {
		root := t.TempDir()
		boot(t, root, now.Add(-10*time.Minute))
		seal(t, root, 512, 512, earlier)
		glue(t, root, resume.GlueResumedGeneration, "512")
		glue(t, root, resume.GlueResumedStep, "512")
		glue(t, root, resume.GlueLoadSeconds, "9.1")
		a, err := resume.Observe(root)
		if err != nil {
			t.Fatal(err)
		}
		if want := "adopted gen 512 (step 512), load 9.1s"; a.Line() != want {
			t.Fatalf("%q, want %q", a.Line(), want)
		}
		if a.ExitCode() != 0 {
			t.Fatalf("exit %d, want 0", a.ExitCode())
		}
	})

	t.Run("cold", func(t *testing.T) {
		root := t.TempDir()
		boot(t, root, now.Add(-10*time.Minute))
		seal(t, root, 512, 512, earlier)
		glue(t, root, resume.GlueStep, "3")
		a, err := resume.Observe(root)
		if err != nil {
			t.Fatal(err)
		}
		if want := "not adopted: step 3, newest sealed 512"; a.Line() != want {
			t.Fatalf("%q, want %q", a.Line(), want)
		}
		if a.ExitCode() != 1 {
			t.Fatalf("exit %d, want 1", a.ExitCode())
		}
	})

	t.Run("older generation", func(t *testing.T) {
		root := t.TempDir()
		boot(t, root, now.Add(-10*time.Minute))
		seal(t, root, 480, 448, earlier)
		seal(t, root, 512, 512, earlier.Add(time.Minute))
		glue(t, root, resume.GlueResumedGeneration, "480")
		glue(t, root, resume.GlueResumedStep, "448")
		a, err := resume.Observe(root)
		if err != nil {
			t.Fatal(err)
		}
		want := "adopted gen 480 (step 448), newest sealed 512 (step 512): 64 steps on disk unused"
		if a.Line() != want {
			t.Fatalf("%q, want %q", a.Line(), want)
		}
		if a.ExitCode() != 1 {
			t.Fatalf("exit %d, want 1", a.ExitCode())
		}
	})

	t.Run("nothing sealed", func(t *testing.T) {
		root := t.TempDir()
		boot(t, root, now)
		a, err := resume.Observe(root)
		if err != nil {
			t.Fatal(err)
		}
		if want := "nothing to adopt: no sealed generation"; a.Line() != want {
			t.Fatalf("%q, want %q", a.Line(), want)
		}
		if a.ExitCode() != 0 {
			t.Fatalf("exit %d, want 0: a first run is not a failure", a.ExitCode())
		}
	})

	t.Run("step matches, generation does not", func(t *testing.T) {
		root := t.TempDir()
		boot(t, root, now.Add(-10*time.Minute))
		seal(t, root, 480, 512, earlier)
		seal(t, root, 512, 512, earlier.Add(time.Minute))
		glue(t, root, resume.GlueResumedGeneration, "480")
		glue(t, root, resume.GlueResumedStep, "512")
		a, err := resume.Observe(root)
		if err != nil {
			t.Fatal(err)
		}
		want := "adopted step 512 from generation 480, newest sealed is 512"
		if a.Line() != want {
			t.Fatalf("%q, want %q", a.Line(), want)
		}
		if a.ExitCode() != 1 {
			t.Fatalf("exit %d, want 1", a.ExitCode())
		}
	})

	t.Run("ahead of the newest seal is not an adoption", func(t *testing.T) {
		root := t.TempDir()
		boot(t, root, now.Add(-10*time.Minute))
		seal(t, root, 512, 512, earlier)
		glue(t, root, resume.GlueResumedGeneration, "512")
		glue(t, root, resume.GlueResumedStep, "520")
		a, err := resume.Observe(root)
		if err != nil {
			t.Fatal(err)
		}
		if a.Reason != resume.AdoptUnknown {
			t.Fatalf("reason %q, want unknown", a.Reason)
		}
		if a.ExitCode() != 2 {
			t.Fatalf("exit %d, want 2", a.ExitCode())
		}
	})
}

// The comparison uses the seal that existed when the job started, not the newest one now: a job that
// resumed at 448 and has since saved 512 adopted 448, and saying otherwise would call a good run a loss.
func TestTheComparisonUsesTheSealThatExistedAtBoot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	boot(t, root, now.Add(-15*time.Minute))
	seal(t, root, 480, 448, now.Add(-2*time.Hour))
	// This one was sealed after the job started: it is the job's own subsequent write.
	seal(t, root, 512, 512, now.Add(-5*time.Minute))
	glue(t, root, resume.GlueResumedGeneration, "480")
	glue(t, root, resume.GlueResumedStep, "448")

	a, err := resume.Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Reason != resume.AdoptAdopted {
		t.Fatalf("reason %q, want adopted: the job resumed from the newest seal that existed at boot", a.Reason)
	}
	if a.Line() != "adopted gen 480 (step 448)" {
		t.Fatalf("line %q", a.Line())
	}
}

func TestAnUnreadableReportIsNotSilence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	boot(t, root, now.Add(-time.Minute))
	seal(t, root, 512, 512, now.Add(-time.Hour))
	glue(t, root, resume.GlueResumedStep, "not-a-number")

	a, err := resume.Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Reason != resume.AdoptUnknown {
		t.Fatalf("reason %q, want unknown: an unreadable report is not a cold start", a.Reason)
	}
	if a.ExitCode() != 2 {
		t.Fatalf("exit %d, want 2", a.ExitCode())
	}
}
