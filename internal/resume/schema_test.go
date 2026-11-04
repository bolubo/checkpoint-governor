package resume

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// full is the declaration every other case is a mutation of. It is built here rather than in a JSON
// literal so the fixture generator and the tests cannot drift in what "a full declaration" means.
func full() File {
	return File{
		Schema:     Schema,
		Generation: 7,
		Step:       512,
		Format:     FormatDCP,
		World:      World{Ranks: 8, TP: 1, PP: 1, DP: 8},
		State: State{
			Model: true, Optimizer: true, Scheduler: true,
			RNG: true, Dataloader: true,
		},
	}
}

func mustJSON(t *testing.T, f File) []byte {
	t.Helper()
	b, err := f.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// The declaration arrives on a customer's volume and is read before a restore, so every rule here is a
// refusal that has to name the offending field: "invalid" without the name is a dead end for the person
// who has to fix it.
func TestTheSchemaRefusesWhatItCannotBelieve(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*File)
		want string
	}{
		{"schema from another build", func(f *File) { f.Schema = 2 }, "says schema 2; this build reads 1"},
		{"no generation", func(f *File) { f.Generation = 0 }, "generation 0 is not positive"},
		{"negative step", func(f *File) { f.Step = -1 }, "step -1 is negative"},
		{"format outside the catalogue", func(f *File) { f.Format = "nemo" }, `format "nemo" is not one we can judge`},
		{"no ranks", func(f *File) { f.World.Ranks = 0 }, "world.ranks is 0"},
		{"negative tp", func(f *File) { f.World.TP = -2 }, "world.tp is -2"},
		{"ranks beyond the bound", func(f *File) { f.World.Ranks = MaxRanks + 1 }, "world.ranks is 65537"},
		{"partial names a non-state", func(f *File) { f.Partial = []string{"shards"} }, `partial names "shards"`},
		{"partial names a state twice", func(f *File) { f.Partial = []string{"optimizer", "optimizer"} }, `partial names "optimizer" twice`},
		{"partial contradicts the file", func(f *File) {
			f.State.Optimizer = false
			f.Partial = []string{"optimizer"}
		}, `partial names "optimizer", but the file does not hold it`},
		{"loader with a newline", func(f *File) { f.Loader = "python resume.py\nrm -rf /" }, "loader holds a control character"},
		{"requires names a non-state", func(f *File) { f.Requires = []string{"shards"} }, `requires names "shards"`},
		{"requires names a state twice", func(f *File) { f.Requires = []string{"model", "model"} }, `requires names "model" twice`},
		{"target is a path", func(f *File) { f.Target = "../other/ckpt" }, `target "../other/ckpt" must be a sibling file name`},
		{"target is absolute", func(f *File) { f.Target = "/tmp/x.ckpt" }, "must be a sibling file name"},
		{"caveat outside the closed set", func(f *File) { f.Caveats = []string{"slow-load"} }, `caveat "slow-load" is not one we can print`},
		{"min_reader from the future", func(f *File) { f.MinReader = "0.25" }, "needs ckptctl >= 0.25 (you have 0.24)"},
		{"min_reader is not a version", func(f *File) { f.MinReader = "next" }, `min_reader "next" is not a version`},
	}
	for _, c := range cases {
		f := full()
		c.mut(&f)
		_, err := Parse(mustJSONRaw(t, f))
		if err == nil {
			t.Errorf("%s: accepted %+v", c.name, f)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not name the problem %q", c.name, err, c.want)
		}
	}
}

// mustJSONRaw encodes a mutated declaration without running the writer's validation. The point of the
// table is the reader, so the bytes have to exist even when the writer would refuse them.
func mustJSONRaw(t *testing.T, f File) []byte {
	t.Helper()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestUnknownFieldsAreRefusedRatherThanIgnored(t *testing.T) {
	good := mustJSON(t, full())
	// "missing" is our conclusion to draw, not the writer's claim; a writer that writes it has
	// misunderstood the file, and the misunderstanding must not travel through a restore unnoticed.
	injected := strings.Replace(string(good), `"schema": 1,`, `"schema": 1, "missing": ["optimizer"],`, 1)
	_, err := Parse([]byte(injected))
	if err == nil {
		t.Fatal("a declaration with the field \"missing\" was accepted")
	}
	if !strings.Contains(err.Error(), `unknown field "missing"`) {
		t.Fatalf("error %q does not name the unknown field", err)
	}
}

func TestASecondJSONValueIsNotAnExtension(t *testing.T) {
	good := mustJSON(t, full())
	_, err := Parse(append(append([]byte{}, good...), good...))
	if err == nil {
		t.Fatal("two declarations in one file were accepted")
	}
	if !strings.Contains(err.Error(), "more than one JSON value") {
		t.Fatalf("error %q does not say what happened", err)
	}
}

func TestAnOversizedDeclarationIsRefusedBeforeItIsAllocated(t *testing.T) {
	pad := strings.Repeat("x", MaxBytes)
	_, err := Parse([]byte(`{"schema":1,"padding":"` + pad + `"}`))
	if err == nil {
		t.Fatal("a declaration past the size bound was accepted")
	}
	if !strings.Contains(err.Error(), "more than the 65536 allowed") {
		t.Fatalf("error %q does not name the bound", err)
	}
}

// The reference writer and the reader have to agree byte for byte, or a fixture generated by one and
// judged by the other would be testing two different files.
func TestADeclarationThatGoesInComesOutIdentical(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "gen-0007"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := full()
	want.Partial = []string{"dataloader"}
	b, err := want.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(dir, "gen-0007"), want); err != nil {
		t.Fatal(err)
	}
	got, present, err := Load(filepath.Join(dir, "gen-0007"))
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("the writer wrote a declaration the reader cannot find")
	}
	again, err := got.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(b) {
		t.Fatalf("round trip changed the bytes:\n-- wrote --\n%s\n-- read --\n%s", b, again)
	}
}

func TestAMissingDeclarationIsUnstatedNotBroken(t *testing.T) {
	_, present, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a generation with no declaration was an error: %v", err)
	}
	if present {
		t.Fatal("a generation with no declaration reported one")
	}
}

// `ext` is the only place an unknown field may live: readers ignore its contents. The discipline
// that made every other unknown field an error is unchanged.
func TestExtIsTheOnlyExtensionPoint(t *testing.T) {
	good := mustJSON(t, full())
	injected := strings.Replace(string(good), `"loader"`, `"ext": {"requires-v2": [1,2]}, "loader"`, 1)
	if _, err := Parse([]byte(injected)); err != nil {
		t.Fatalf("ext was refused: %v", err)
	}
}

// The version-skew matrix in one place: a declaration from a newer build is refused with the upgrade
// named, and a declaration from an older build gets exactly the answer it got then.
func TestTheVersionSkewMatrix(t *testing.T) {
	// Old engine, new declaration: refuse, and say which version is needed.
	f := full()
	f.MinReader = "0.26"
	_, err := Parse(mustJSONRaw(t, f))
	if err == nil {
		t.Fatal("a declaration written for a newer build was accepted")
	}
	if !strings.Contains(err.Error(), "needs ckptctl >= 0.26 (you have 0.24)") {
		t.Fatalf("error %q does not name the upgrade", err)
	}

	// New engine, old declaration: no new fields, same verdict as before the fields existed.
	old := mustJSON(t, full())
	if !strings.Contains(string(old), `"generation"`) || strings.Contains(string(old), `"requires"`) {
		t.Fatalf("the reference writer grew a field: %s", old)
	}
	v := Judge(full(), true, nil, World{})
	if v.Line() != "resumable (gen 7, step 512, world 8/8, full state)" {
		t.Fatalf("an old declaration changed its verdict: %q", v.Line())
	}
}

// A declaration the writer could not finish is a declaration that was never made. The reader has to be
// able to tell that from a shorter, valid one — which is why the writer renames.
func TestATruncatedDeclarationDoesNotParse(t *testing.T) {
	b := mustJSON(t, full())
	_, err := Parse(b[:len(b)/2])
	if err == nil {
		t.Fatal("a truncated declaration was accepted")
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Fatalf("error %q does not name the file", err)
	}
}
