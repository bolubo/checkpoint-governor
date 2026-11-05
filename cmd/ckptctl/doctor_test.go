package main

import (
	"strings"
	"testing"
)

// doctor is the answer to "is it watching me, or is it blind?", so it must always be able to run and
// must always print a summary, even on a machine where most conditions are absent.
func TestDoctorAlwaysPrintsASummary(t *testing.T) {
	var out strings.Builder
	code := runDoctor([]string{"--dir", t.TempDir()}, &out)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	text := out.String()
	for _, want := range []string{"store.low-space", "job.ending", "doctor:", "absent"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor is missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "no complete checkpoint yet") {
		t.Fatalf("a directory without a checkpoint must say so:\n%s", text)
	}
}

func TestDoctorNeedsADirectory(t *testing.T) {
	var out strings.Builder
	if code := runDoctor(nil, &out); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "--dir") {
		t.Fatalf("usage was not printed:\n%s", out.String())
	}
}

// The storage question is asked before a deployment, when there is no checkpoint directory yet, so
// --storage alone has to be a complete invocation.
func TestDoctorStorageNeedsNoCheckpointDirectory(t *testing.T) {
	var out strings.Builder
	code := runDoctor([]string{"--storage", t.TempDir()}, &out)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 on a writable local directory:\n%s", code, out.String())
	}
	text := out.String()
	for _, want := range []string{"storage ", "filesystem", "rename", "visible", "size/tail", "class", "promise"} {
		if !strings.Contains(text, want) {
			t.Fatalf("storage report is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "store.low-space") {
		t.Fatalf("a storage-only run must not print the probe table:\n%s", text)
	}
}

func TestDoctorStorageMissingDirectoryIsAnErrorWithAReason(t *testing.T) {
	var out strings.Builder
	if code := runDoctor([]string{"--storage", t.TempDir() + "/gone"}, &out); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "cannot probe it") {
		t.Fatalf("a missing directory must name the reason:\n%s", out.String())
	}
}

// With both flags the environment table still runs after the storage table: the storage question does
// not replace the controller's view, it qualifies it.
func TestDoctorRunsBothSections(t *testing.T) {
	var out strings.Builder
	dir := t.TempDir()
	if code := runDoctor([]string{"--dir", dir, "--storage", dir}, &out); code != 0 {
		t.Fatalf("exit = %d:\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "storage ") || !strings.Contains(text, "doctor:") {
		t.Fatalf("both sections were expected:\n%s", text)
	}
}
