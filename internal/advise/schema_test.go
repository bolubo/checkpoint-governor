package advise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePlan puts a plan file on disk with the given schema, using only the fields that schema had.
func writePlan(t *testing.T, dir string, schema int) string {
	t.Helper()
	base := filepath.Join(dir, BaseDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := map[string]any{"schema": schema, "epoch": 7, "unitId": "job", "action": "skip",
		"expiresAt": time.Now().Add(time.Minute).Unix()}
	if schema == 1 {
		// Schema 1 carried a period, not the two numbers: no requestId, no baseline, no window.
		delete(plan, "expiresAt")
		plan["fallbackIntervalSeconds"] = 600
	}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, stateName)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A plan written by a governor that speaks another contract is not this governor's plan.
//
// The danger is not a crash: it is a file that parses. Schema 1's expiresAt was a period rather than a
// liveness stamp, so a schema-2 binary reads a number that means something else and answers a
// question the file never asked.
func TestAPlanFromAnotherSchemaIsNotAPlan(t *testing.T) {
	for _, schema := range []int{1, 3} {
		dir := t.TempDir()
		writePlan(t, dir, schema)

		if _, ok := ReadPublished(dir); ok {
			t.Fatalf("schema %d: ReadPublished accepted a plan it cannot interpret", schema)
		}
		if _, ok := LastDecision(dir); ok {
			t.Fatalf("schema %d: LastDecision accepted a plan it cannot interpret", schema)
		}

		problem := SchemaProblem(dir)
		if problem == "" {
			t.Fatalf("schema %d: refused the plan and said nothing; a silent refusal cannot be fixed", schema)
		}
		if !strings.Contains(problem, "schema "+itoa(schema)) || !strings.Contains(problem, itoa(SchemaVersion)) {
			t.Fatalf("schema %d: %q must name the version found and the version written", schema, problem)
		}
	}
}

// The current schema still reads, and a missing file is not a problem: an absent plan is a normal
// deployment, not a fault.
func TestTheCurrentSchemaReadsAndAnAbsentOneIsQuiet(t *testing.T) {
	dir := t.TempDir()
	writePlan(t, dir, SchemaVersion)

	p, ok := ReadPublished(dir)
	if !ok {
		t.Fatal("the schema this binary writes was refused")
	}
	if p.Epoch != 7 || p.Decision.UnitID != "job" {
		t.Fatalf("read back the wrong plan: %+v", p)
	}
	if problem := SchemaProblem(dir); problem != "" {
		t.Fatalf("a readable plan reported a problem: %q", problem)
	}

	empty := t.TempDir()
	if problem := SchemaProblem(empty); problem != "" {
		t.Fatalf("no file must stay quiet, got %q", problem)
	}
}

// Renew re-stamps the plan it read. Given a foreign file it must write nothing: a re-stamp would
// produce a file carrying the old schema and the new fields, which is neither version and belongs to
// no reader.
func TestRenewDoesNotRestampAForeignPlan(t *testing.T) {
	dir := t.TempDir()
	path := writePlan(t, dir, 1)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	wrote, err := Renew(dir, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("Renew rewrote a plan from another schema")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the foreign plan changed on disk")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
