package conditions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce"
)

const goodFile = `{
  "name": "job.loss-exploded",
  "tier": "urgent",
  "missing": "urgent",
  "why": "the loss left its normal range",
  "read": {"file": "team-signal", "on": "1", "off": "0"}
}`

// writeSpec puts one file in the directory conditions.d, which is where an operator would put it.
func writeSpec(t *testing.T, dir, name, body string) {
	t.Helper()
	base := filepath.Join(dir, advise.BaseDir, DirName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// One file becomes one row and one probe: the two halves of a condition cannot get out of step
// because they come from the same file.
func TestOneFileBecomesARowAndAProbe(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "team.json", goodFile)

	set, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 1 || len(set.Probes) != 1 {
		t.Fatalf("rows=%d probes=%d, want 1 and 1", set.Len(), len(set.Probes))
	}
	row := set.Rows[0]
	if row.Name != "job.loss-exploded" || row.Tier != policy.TierUrgent || row.Missing != policy.MissingUrgent {
		t.Fatalf("row = %+v", row)
	}
	if set.Probes[0].Name() != row.Name {
		t.Fatalf("probe %s does not match row %s", set.Probes[0].Name(), row.Name)
	}

	// The probe reads the job's file, not the governor's state: the job owns the signal.
	now := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(dir, "team-signal"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := set.Probes[0].Read(produce.Env{Dir: dir, Now: now})
	if r.State != produce.Fresh || !r.Value {
		t.Fatalf("reading = %+v, want fresh true", r)
	}
	if err := os.WriteFile(filepath.Join(dir, "team-signal"), []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := set.Probes[0].Read(produce.Env{Dir: dir, Now: now}); r.State != produce.Fresh || r.Value {
		t.Fatalf("reading = %+v, want fresh false", r)
	}

	// A value that is neither on nor off, and a missing file, are unknown: the fail-closed direction.
	if err := os.WriteFile(filepath.Join(dir, "team-signal"), []byte("maybe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := set.Probes[0].Read(produce.Env{Dir: dir, Now: now}); r.State != produce.Unknown {
		t.Fatalf("reading = %+v, want unknown for a value that is neither on nor off", r)
	}
	if err := os.Remove(filepath.Join(dir, "team-signal")); err != nil {
		t.Fatal(err)
	}
	if r := set.Probes[0].Read(produce.Env{Dir: dir, Now: now}); r.State != produce.Unknown {
		t.Fatalf("reading = %+v, want unknown for a missing file", r)
	}
}

// No directory is the default deployment, not an error.
func TestNoDirectoryAddsNothing(t *testing.T) {
	set, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 0 {
		t.Fatalf("an empty directory added %d conditions", set.Len())
	}
}

// Every refusal below is a startup failure, because each one would leave an operator believing a
// condition is watched when it is not.
func TestBadFilesAreRefused(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"shadow", strings.Replace(goodFile, `job.loss-exploded`, `risk.shutdown`, 1), "built-in"},
		{"tier", strings.Replace(goodFile, `"tier": "urgent"`, `"tier": "loud"`, 1), "tier"},
		{"missing", strings.Replace(goodFile, `"missing": "urgent"`, `"missing": "maybe"`, 1), "missing"},
		{"why", strings.Replace(goodFile, `"why": "the loss left its normal range"`, `"why": ""`, 1), "why"},
		{"onoff", strings.Replace(goodFile, `"on": "1", "off": "0"`, `"on": "1", "off": "1"`, 1), "same value"},
		{"absolute", strings.Replace(goodFile, `"file": "team-signal"`, `"file": "/etc/passwd"`, 1), "inside"},
		{"typo", strings.Replace(goodFile, `"tier"`, `"teir"`, 1), "unknown field"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSpec(t, dir, "bad.json", c.body)
			_, err := Load(dir)
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not say %q", err, c.want)
			}
		})
	}
}

// Two files cannot define the same name: the second would silently replace the first, and "which
// one is watched" would depend on the filesystem's sort order.
func TestTwoFilesCannotDefineTheSameName(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "a.json", goodFile)
	writeSpec(t, dir, "b.json", goodFile)

	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "already defined") {
		t.Fatalf("duplicate names were accepted: %v", err)
	}
}
