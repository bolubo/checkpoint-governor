package resume_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/resume"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// The fixtures are the contract's evidence: each one is a tree the real trainer left behind, and each
// one has to land where its README says it lands. Two things are checked for all of them — the manifest
// parses and its shards are the size it claims, and the declaration (when there is one) describes the
// same generation and step. The second check is what makes a hand-edit fail: a declaration that names a
// different checkpoint is not a fixture, it is a lie with a manifest next to it.
func TestEveryFixtureSaysWhatItIs(t *testing.T) {
	cases := []struct {
		name string
		// want is the text the fixture's README must carry, or "" for "no declaration at all".
		want string
		// bad is non-empty when the declaration must fail to parse, with this substring.
		bad string
	}{
		{"full", "**resumable**", ""},
		{"weights-only", "**not resumable: optimizer state absent**", ""},
		{"partial-optimizer", "**not resumable: declared partial (optimizer)**", ""},
		{"reshardable", "**resumable with resharding**", ""},
		{"unknown-format", `**unknown: format "nemo" is not one we can judge**`, "not one we can judge"},
		{"no-resume", "**unknown: sealed, but nobody said what it holds**", ""},
		{"invalid", "**unknown: resume.json does not parse**", "resume.json"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join("testdata", c.name)
			gen := singleGeneration(t, dir)

			m := sealedManifest(t, gen)
			for _, s := range m.Shards {
				info, err := os.Stat(filepath.Join(gen, s.Name))
				if err != nil {
					t.Fatalf("manifest names %q, which is not on disk: %v", s.Name, err)
				}
				if info.Size() != s.Size {
					t.Fatalf("%q is %d bytes, manifest claims %d", s.Name, info.Size(), s.Size)
				}
			}

			readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
			if err != nil {
				t.Fatalf("fixture has no README: %v", err)
			}
			if !strings.Contains(string(readme), c.want) {
				t.Fatalf("README does not claim %q", c.want)
			}

			f, present, err := resume.Load(gen)
			if c.bad != "" {
				if !present {
					t.Fatalf("fixture %q must hold a declaration", c.name)
				}
				if err == nil || !strings.Contains(err.Error(), c.bad) {
					t.Fatalf("a declaration that %q was accepted (%v)", c.bad, err)
				}
				return
			}
			if c.name == "no-resume" {
				if present || err != nil {
					t.Fatalf("no-resume reported a declaration (present=%v, err=%v)", present, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("declaration does not parse: %v", err)
			}
			if f.Generation != m.Generation || f.Step != m.Step {
				t.Fatalf("declaration says gen %d step %d; manifest says gen %d step %d",
					f.Generation, f.Step, m.Generation, m.Step)
			}
			raw, err := os.ReadFile(filepath.Join(gen, resume.FileName))
			if err != nil {
				t.Fatal(err)
			}
			again, err := f.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(raw) {
				t.Fatalf("the checked-in file is not what the writer produces; regenerate with " +
					"`sh hack/gen-resume-fixtures.sh`")
			}
		})
	}
}

// singleGeneration finds the one generation directory a fixture holds: a second one would mean the
// fixture's verdict is no longer about one checkpoint.
func singleGeneration(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var gens []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gen-") && e.IsDir() {
			gens = append(gens, filepath.Join(dir, e.Name()))
		}
	}
	if len(gens) != 1 {
		t.Fatalf("%s holds %d generation directories, want exactly one", dir, len(gens))
	}
	return gens[0]
}

func sealedManifest(t *testing.T, gen string) verify.Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(gen, "manifest.json"))
	if err != nil {
		t.Fatalf("fixture has no sealed manifest: %v", err)
	}
	m, err := verify.Parse(raw)
	if err != nil {
		t.Fatalf("manifest does not parse: %v", err)
	}
	if m.Writer != "ckpttrainer" {
		t.Fatalf("fixture was not written by the trainer (writer %q)", m.Writer)
	}
	return m
}
