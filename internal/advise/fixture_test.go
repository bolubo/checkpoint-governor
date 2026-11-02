package advise

import (
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// A plan written by the previous build of this binary still reads, and still means the same thing.
//
// This is the compatibility promise, and it is checked against a directory the real binaries wrote
// (testdata/schema-2, see its README): editing the fixture by hand would prove that the parser agrees
// with the editor, which is not the property a user needs.
func TestAPlanFromThePreviousVersionIsReadAgain(t *testing.T) {
	const fixture = "testdata/schema-2/job-1"

	p, ok := ReadPublished(fixture)
	if !ok {
		t.Fatalf("the fixture's plan was refused: %q", SchemaProblem(fixture))
	}
	if p.Epoch != 1 {
		t.Fatalf("epoch = %d, want the recorded 1: an old plan is adopted, not reset", p.Epoch)
	}
	d := p.Decision
	if d.Action != policy.ActionSkip || d.Reason != policy.ReasonCalm {
		t.Fatalf("decision = %s/%s, want skip/calm", d.Action, d.Reason)
	}
	if d.Why != "not starting a write: already-saved" {
		t.Fatalf("why = %q: the sentence is part of what a reader sees", d.Why)
	}
	if d.Version != "risk-v1" {
		t.Fatalf("version = %q, want risk-v1", d.Version)
	}
	if problem := SchemaProblem(fixture); problem != "" {
		t.Fatalf("the fixture was reported as a foreign schema: %s", problem)
	}

	t.Logf("adopted: schema %d, decision %s (%s), epoch %d, version %s",
		SchemaVersion, d.Action, d.Reason, p.Epoch, d.Version)
}

// The plan's schema is the first thing a reader can check, and it is readable without decoding the
// whole contract: a foreign file may not fit today's structure.
func TestThePlanSchemaIsReadableOnItsOwn(t *testing.T) {
	if schema, ok := PlanSchema("testdata/schema-2/job-1"); !ok || schema != SchemaVersion {
		t.Fatalf("PlanSchema = %d, %v; want %d, true", schema, ok, SchemaVersion)
	}
	if schema, ok := PlanSchema(t.TempDir()); ok || schema != 0 {
		t.Fatalf("a directory without a plan reported schema %d, %v", schema, ok)
	}
	if Reads() != "2" {
		t.Fatalf("Reads() = %q, want the one number this build reads", Reads())
	}
}
