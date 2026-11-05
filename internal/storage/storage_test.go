package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On a local temporary directory every core check must pass, and the report must name the promise.
// The exact tier depends on the runner's filesystem, so the test asserts the contract, not the label
// the CI happen to run on.
func TestProbeAPlainDirectory(t *testing.T) {
	r, err := Probe(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Checks) != 5 {
		t.Fatalf("checks = %d, want 5 (%+v)", len(r.Checks), r.Checks)
	}
	for _, c := range r.Checks {
		if c.Name == "fsync dir" {
			continue // a directory sync can be refused by a memory filesystem; it is reported, not fatal
		}
		if !c.OK {
			t.Fatalf("%s failed on a plain directory: %s", c.Name, c.Detail)
		}
	}
	if r.Class != ClassLocal && r.Class != ClassSharedSafe && r.Class != ClassSharedUnsafe {
		t.Fatalf("class = %q", r.Class)
	}
	if !strings.Contains(r.Promise, "process death") && r.Class != ClassSharedUnsafe {
		t.Fatalf("promise = %q", r.Promise)
	}
}

// The probe must not leave its scratch directory behind: doctor is run on production roots.
func TestProbeLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	if _, err := Probe(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ckptctl-storage-") {
			t.Fatalf("probe left %s behind", e.Name())
		}
	}
}

func TestProbeAFileIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(path); err == nil {
		t.Fatal("probing a file must fail")
	}
}

func TestProbeAMissingDirectoryNamesTheError(t *testing.T) {
	if _, err := Probe(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("probing a missing directory must fail")
	}
}

// The classifier is the promise: a failed check must downgrade even a local disk, and a shared disk
// needs the directory entry synced before it may claim node survival.
func TestTheTierIsDecidedByChecksNotByName(t *testing.T) {
	cases := []struct {
		name                                         string
		network                                      bool
		fileSync, rename, visible, sizeTail, dirSync bool
		want                                         string
	}{
		{"local, all checks pass", false, true, true, true, true, true, ClassLocal},
		{"local, rename failed", false, true, false, true, true, true, ClassSharedUnsafe},
		{"local, invisible to the second process", false, true, true, false, true, true, ClassSharedUnsafe},
		{"shared, all checks pass", true, true, true, true, true, true, ClassSharedSafe},
		{"shared, directory entry not synced", true, true, true, true, true, false, ClassSharedUnsafe},
		{"shared, tail mismatch", true, true, true, true, false, true, ClassSharedUnsafe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Report{Network: c.network}
			r.Checks = []Check{
				{Name: "fsync file", OK: c.fileSync},
				{Name: "rename", OK: c.rename},
				{Name: "visible", OK: c.visible},
				{Name: "size/tail", OK: c.sizeTail},
				{Name: "fsync dir", OK: c.dirSync},
			}
			got := finish(r)
			if got.Class != c.want {
				t.Fatalf("class = %q, want %q (checks %+v)", got.Class, c.want, got.Checks)
			}
			if got.Promise == "" {
				t.Fatal("a tier without a promise is a table nobody can act on")
			}
		})
	}
}

func TestReportWriteNamesEveryFailedReason(t *testing.T) {
	r := Report{
		Dir:     "/ckpt",
		FSName:  "nfs",
		Network: true,
		Checks:  []Check{{Name: "rename", Detail: "same-directory rename failed: EACCES"}},
		Class:   ClassSharedUnsafe,
		Promise: promiseFor(ClassSharedUnsafe),
	}
	var b strings.Builder
	r.Write(&b)
	text := b.String()
	for _, want := range []string{"storage /ckpt", "nfs", "rename", "failed", "EACCES", "shared-unsafe", "promise"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report is missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "do not resume") {
		t.Fatalf("an unsafe tier must say what not to do:\n%s", text)
	}
}
