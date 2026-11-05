package main

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func demoDir(t *testing.T, scenario string) string {
	t.Helper()
	dir := t.TempDir()
	if code := runDemo([]string{"--dir", dir, "--scenario", scenario}, io.Discard); code != 0 {
		t.Fatalf("demo setup failed: %d", code)
	}
	return dir
}

func TestEvidenceTextNamesTheProblem(t *testing.T) {
	dir := demoDir(t, "kill")
	var out strings.Builder
	if code := runEvidence([]string{"--dir", dir}, &out); code != 0 {
		t.Fatalf("evidence = %d: %s", code, out.String())
	}
	text := out.String()
	for _, want := range []string{"schema 1", "not resumable: rank 1 shard missing", "problems    1", "shard_missing"} {
		if !strings.Contains(text, want) {
			t.Fatalf("evidence is missing %q:\n%s", want, text)
		}
	}
}

// The JSON bundle must carry the same verdict as the command it comes from: an evidence bundle that
// disagrees with `resumable --json` would be a second answer to one question.
func TestEvidenceJSONAgreesWithResumable(t *testing.T) {
	dir := demoDir(t, "kill")

	var evidenceOut strings.Builder
	if code := runEvidence([]string{"--dir", dir, "--json"}, &evidenceOut); code != 0 {
		t.Fatalf("evidence = %d", code)
	}
	var bundle struct {
		Schema int `json:"schema"`
		Resume struct {
			State      string `json:"state"`
			Reason     string `json:"reason"`
			Generation int64  `json:"generation"`
			Step       int64  `json:"step"`
		} `json:"resume"`
	}
	if err := json.Unmarshal([]byte(evidenceOut.String()), &bundle); err != nil {
		t.Fatalf("evidence --json: %v\n%s", err, evidenceOut.String())
	}
	if bundle.Schema != 1 || bundle.Resume.State != "not resumable" {
		t.Fatalf("bundle = %+v", bundle)
	}

	var resumableOut strings.Builder
	if code := runResumable([]string{"--dir", dir, "--json"}, &resumableOut); code != 1 {
		t.Fatalf("resumable = %d, want 1", code)
	}
	var verdict struct {
		Reason     string `json:"Reason"`
		Generation int64  `json:"Generation"`
		Step       int64  `json:"Step"`
	}
	if err := json.Unmarshal([]byte(resumableOut.String()), &verdict); err != nil {
		t.Fatalf("resumable --json: %v\n%s", err, resumableOut.String())
	}
	if verdict.Reason != bundle.Resume.Reason || verdict.Generation != bundle.Resume.Generation || verdict.Step != bundle.Resume.Step {
		t.Fatalf("bundle disagrees with resumable: %+v vs %+v", bundle.Resume, verdict)
	}
}

func TestVerifyJSON(t *testing.T) {
	dir := demoDir(t, "torn")
	var out strings.Builder
	code := runVerify([]string{"--dir", filepath.Join(dir, "gen-0001"), "--json"}, &out)
	if code != 1 {
		t.Fatalf("verify = %d, want 1: %s", code, out.String())
	}
	var got struct {
		Schema int    `json:"schema"`
		State  string `json:"state"`
		Reason string `json:"reason"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("verify --json: %v\n%s", err, out.String())
	}
	if got.Schema != 1 || got.State != "broken" || !strings.Contains(got.Detail, "tail hashes") {
		t.Fatalf("verdict = %+v", got)
	}
}

func TestDoctorJSONCarriesStorageAndProbes(t *testing.T) {
	dir := demoDir(t, "clean")
	var out strings.Builder
	if code := runDoctor([]string{"--dir", dir, "--storage", dir, "--json"}, &out); code != 0 {
		t.Fatalf("doctor = %d: %s", code, out.String())
	}
	var got struct {
		Schema  int `json:"schema"`
		Storage struct {
			Class  string `json:"class"`
			Checks []struct {
				Name string `json:"name"`
				OK   bool   `json:"ok"`
			} `json:"checks"`
		} `json:"storage"`
		Probes []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"probes"`
		Summary struct {
			Probes int `json:"probes"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out.String())
	}
	if got.Schema != 1 || got.Storage.Class == "" || len(got.Storage.Checks) != 5 {
		t.Fatalf("storage = %+v", got.Storage)
	}
	if len(got.Probes) == 0 || got.Summary.Probes != len(got.Probes) {
		t.Fatalf("probes = %d, summary = %d", len(got.Probes), got.Summary.Probes)
	}
}

func TestExplainJSONWithoutAPlan(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if code := runExplain([]string{"--dir", dir, "--json"}, &out); code != 1 {
		t.Fatalf("explain = %d, want 1", code)
	}
	var got struct {
		Schema int    `json:"schema"`
		Dir    string `json:"dir"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("explain --json: %v\n%s", err, out.String())
	}
	if got.Schema != 1 || got.Dir != dir {
		t.Fatalf("payload = %+v", got)
	}
}
