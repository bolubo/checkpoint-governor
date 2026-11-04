// Package resume reads the file a checkpoint writer leaves beside its manifest, and says what a
// restore would actually get. It never writes a checkpoint: the only file it writes is the declaration
// itself, and only when a writer asks it to (decision 0026).
//
// The declaration exists because "the bytes are whole" and "the work can be resumed" are different
// claims (0025). A sealed copy can be perfectly intact and still be unresumable — a library that saved
// only the weights, a world size that changed under a format that cannot be re-sharded, an optimizer
// state its own author excluded. Those are the cases this package names.
package resume

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Schema is the version of the declaration this build reads and writes. It is separate from the plan
// schema (which stays 2) and from the manifest, which is a different file with a different owner.
const Schema = 1

// FileName is the declaration, inside a generation directory, written after the manifest is sealed.
const FileName = "resume.json"

// Bounds. The file arrives on the customer's volume, written by a library we do not control, and it is
// read on every restore: an unbounded parser here is an allocation an operator cannot see coming.
const (
	MaxBytes       = 64 << 10
	MaxRanks       = 65536
	MaxPartial     = 16
	MaxRequires    = 7
	MaxCaveats     = 8
	MaxTargetBytes = 255
	MaxLoaderBytes = 512
)

// ReaderVersion is what this build can judge. A writer may set `min_reader` to the version its
// declaration needs; a newer declaration read by an older build is refused with the upgrade named,
// rather than misread as an older one (decision 0026, version skew).
const ReaderVersion = "0.24"

// Formats are the layouts a declaration may name. The list is closed for the same reason the condition
// catalogue is: a typo must be refused at read time, not silently treated as "some other format".
const (
	FormatDCP         = "dcp"
	FormatTorchDist   = "torch_dist"
	FormatSafetensors = "safetensors"
	FormatHFTrainer   = "hf-trainer"
	FormatOther       = "other"
)

// State is what the checkpoint actually holds. `false` means the writer did not save it, and a restore
// that needs it cannot be a full resume — which is the difference between "resumed" and "a new run with
// old weights" (decision 0026).
type State struct {
	Model      bool `json:"model"`
	Optimizer  bool `json:"optimizer"`
	Scheduler  bool `json:"scheduler"`
	RNG        bool `json:"rng"`
	Dataloader bool `json:"dataloader"`
	Scaler     bool `json:"scaler"`
	EMA        bool `json:"ema"`
}

// present lists what the file says it holds, in the catalogue's order. The order is fixed so two
// verdicts for the same facts are byte-identical, which is what makes them comparable in a receipt.
func (s State) present() []string {
	var out []string
	for _, e := range stateOrder {
		if e.held(s) {
			out = append(out, e.name)
		}
	}
	return out
}

type stateEntry struct {
	name string
	held func(State) bool
}

var stateOrder = []stateEntry{
	{"model", func(s State) bool { return s.Model }},
	{"optimizer", func(s State) bool { return s.Optimizer }},
	{"scheduler", func(s State) bool { return s.Scheduler }},
	{"rng", func(s State) bool { return s.RNG }},
	{"dataloader", func(s State) bool { return s.Dataloader }},
	{"scaler", func(s State) bool { return s.Scaler }},
	{"ema", func(s State) bool { return s.EMA }},
}

// stateNames is the closed set `partial` may name.
var stateNames = map[string]bool{}

func init() {
	for _, e := range stateOrder {
		stateNames[e.name] = true
	}
}

// World is the topology the checkpoint was written under, and whether it can be read under another.
//
// A zero field means "not declared". The distinction matters: Megatron's default optimizer format is not
// re-shardable at all, so a changed world size is a refusal rather than a cost — and guessing either way
// would be a lie (decision 0026).
type World struct {
	Ranks       int  `json:"ranks"`
	TP          int  `json:"tp"`
	PP          int  `json:"pp"`
	DP          int  `json:"dp"`
	Reshardable bool `json:"reshardable"`
}

// File is the declaration. It says what a restore would get, and what it would have to give up.
type File struct {
	Schema     int      `json:"schema"`
	Generation int64    `json:"generation"`
	Step       int64    `json:"step"`
	Format     string   `json:"format"`
	World      World    `json:"world"`
	State      State    `json:"state"`
	Partial    []string `json:"partial,omitempty"`
	// Requires is which states a resume of *this* job needs. Absent means the catalogue's default
	// (model, optimizer, scheduler, rng, dataloader). Hugging Face and Lightning do not save a
	// dataloader position — they re-iterate and skip — so an integration declares four, not five.
	// Without this field every framework checkpoint would be judged against a requirement its
	// framework never had (0026).
	Requires []string `json:"requires,omitempty"`
	// Target is which artifact this declaration describes: "." for the directory it sits in (Hugging
	// Face's checkpoint-N/), or a sibling file name (Lightning's .ckpt, a Composer episode). Absent
	// means "." — the only place a declaration lived before this field existed.
	Target string `json:"target,omitempty"`
	// Caveats are facts a person must hear that do not change "can it continue": today the closed
	// set is one value, `data-position-not-saved`.
	Caveats []string `json:"caveats,omitempty"`
	// MinReader is the build a writer says a reader needs. A lower build refuses instead of guessing.
	MinReader string `json:"min_reader,omitempty"`
	// Ext is the only extension point: readers ignore its contents and say so in `status`. Everything
	// else unknown is still refused, because a typo must not travel through a restore unnoticed.
	Ext map[string]json.RawMessage `json:"ext,omitempty"`
	// Loader is the command the writer suggests for a drill. It is never executed because it was
	// declared here: an untrusted file must not become a command (decision 0026).
	Loader string `json:"loader,omitempty"`
}

// Parse reads a declaration and refuses anything it cannot believe.
//
// Unknown fields are an error rather than an extension point: a writer that writes "missing" (which is
// our conclusion to draw, not its claim) has misunderstood the file, and ignoring it would let the
// misunderstanding travel through a restore unnoticed.
func Parse(b []byte) (File, error) {
	if len(b) > MaxBytes {
		return File{}, fmt.Errorf("%s is %d bytes, more than the %d allowed", FileName, len(b), MaxBytes)
	}
	var f File
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	// One declaration per file. A second object after the first is not an extension: it is two writers'
	// answers in one place, and picking the first would hide the disagreement.
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return File{}, fmt.Errorf("%s holds more than one JSON value", FileName)
	}
	if err := f.validate(); err != nil {
		return File{}, err
	}
	return f, nil
}

func (f File) validate() error {
	if f.Schema != Schema {
		return fmt.Errorf("%s says schema %d; this build reads %d", FileName, f.Schema, Schema)
	}
	if f.Generation <= 0 {
		return fmt.Errorf("%s: generation %d is not positive", FileName, f.Generation)
	}
	if f.Step < 0 {
		return fmt.Errorf("%s: step %d is negative", FileName, f.Step)
	}
	switch f.Format {
	case FormatDCP, FormatTorchDist, FormatSafetensors, FormatHFTrainer, FormatOther:
	default:
		return fmt.Errorf("%s: format %q is not one we can judge (known: %s)", FileName, f.Format, strings.Join(formats(), ", "))
	}
	if err := f.World.validate(); err != nil {
		return err
	}
	if err := f.validatePartial(); err != nil {
		return err
	}
	if err := f.validateRequires(); err != nil {
		return err
	}
	if err := f.validateTarget(); err != nil {
		return err
	}
	if err := f.validateCaveats(); err != nil {
		return err
	}
	if err := f.validateMinReader(); err != nil {
		return err
	}
	if len(f.Loader) > MaxLoaderBytes {
		return fmt.Errorf("%s: loader is %d bytes, more than the %d allowed", FileName, len(f.Loader), MaxLoaderBytes)
	}
	for _, r := range f.Loader {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s: loader holds a control character", FileName)
		}
	}
	return nil
}

func (w World) validate() error {
	for _, e := range []struct {
		name string
		n    int
	}{{"ranks", w.Ranks}, {"tp", w.TP}, {"pp", w.PP}, {"dp", w.DP}} {
		if e.n < 0 || e.n > MaxRanks {
			return fmt.Errorf("%s: world.%s is %d, outside 0..%d", FileName, e.name, e.n, MaxRanks)
		}
	}
	if w.Ranks == 0 {
		return fmt.Errorf("%s: world.ranks is 0; a checkpoint was written by some number of ranks", FileName)
	}
	return nil
}

// validatePartial refuses a declaration whose two halves contradict each other: a name it says the loader
// will skip, in a file that does not hold it, is not a partial restore — it is a writer that is guessing.
func (f File) validatePartial() error {
	if len(f.Partial) > MaxPartial {
		return fmt.Errorf("%s: partial names %d entries, more than the %d allowed", FileName, len(f.Partial), MaxPartial)
	}
	seen := map[string]bool{}
	for _, name := range f.Partial {
		if !stateNames[name] {
			return fmt.Errorf("%s: partial names %q, which is not a state this file can carry", FileName, name)
		}
		if seen[name] {
			return fmt.Errorf("%s: partial names %q twice", FileName, name)
		}
		seen[name] = true
		if !f.State.holds(name) {
			return fmt.Errorf("%s: partial names %q, but the file does not hold it", FileName, name)
		}
	}
	return nil
}

// validateRequires refuses a requirement this file cannot answer, and a duplicate. An absent list is
// valid: it is the default five, which is what every declaration written before the field existed says.
func (f File) validateRequires() error {
	if len(f.Requires) > MaxRequires {
		return fmt.Errorf("%s: requires names %d states, more than the %d possible", FileName, len(f.Requires), MaxRequires)
	}
	seen := map[string]bool{}
	for _, name := range f.Requires {
		if !stateNames[name] {
			return fmt.Errorf("%s: requires names %q, which is not a state this file can carry", FileName, name)
		}
		if seen[name] {
			return fmt.Errorf("%s: requires names %q twice", FileName, name)
		}
		seen[name] = true
	}
	return nil
}

// validateTarget refuses anything but the two places a declaration may live: the directory it sits in
// (".") or a sibling artifact's name. A path would make the declaration describe something else, which
// is the one thing the format exists to prevent.
func (f File) validateTarget() error {
	if f.Target == "" || f.Target == "." {
		return nil
	}
	if len(f.Target) > MaxTargetBytes {
		return fmt.Errorf("%s: target is %d bytes, more than the %d allowed", FileName, len(f.Target), MaxTargetBytes)
	}
	if strings.ContainsAny(f.Target, `/\`) || f.Target == ".." || strings.HasPrefix(f.Target, "..") {
		return fmt.Errorf("%s: target %q must be a sibling file name or \".\", not a path", FileName, f.Target)
	}
	return nil
}

// CaveatNames is the closed set `caveats` may name. A caveat does not change the verdict, so a typo
// would silently drop the one fact the user needed to hear.
var CaveatNames = map[string]bool{
	CaveatDataPositionNotSaved: true,
}

// CaveatDataPositionNotSaved is the only caveat today: the copy can continue, but the dataset position
// was not saved, so a restore replays or restarts the epoch.
const CaveatDataPositionNotSaved = "data-position-not-saved"

func (f File) validateCaveats() error {
	if len(f.Caveats) > MaxCaveats {
		return fmt.Errorf("%s: caveats names %d entries, more than the %d allowed", FileName, len(f.Caveats), MaxCaveats)
	}
	seen := map[string]bool{}
	for _, name := range f.Caveats {
		if !CaveatNames[name] {
			return fmt.Errorf("%s: caveat %q is not one we can print (known: %s)", FileName, name, CaveatDataPositionNotSaved)
		}
		if seen[name] {
			return fmt.Errorf("%s: caveat %q twice", FileName, name)
		}
		seen[name] = true
	}
	return nil
}

// validateMinReader refuses a declaration written for a newer build. Reading it as though it were
// this schema is the failure mode the field exists to stop: the file may hold fields this build
// ignores, and an ignored field is indistinguishable from an absent one.
func (f File) validateMinReader() error {
	if f.MinReader == "" {
		return nil
	}
	need, ok := parseVersion(f.MinReader)
	if !ok {
		return fmt.Errorf("%s: min_reader %q is not a version (want MAJOR.MINOR)", FileName, f.MinReader)
	}
	if mine, _ := parseVersion(ReaderVersion); need.major > mine.major ||
		(need.major == mine.major && need.minor > mine.minor) {
		return fmt.Errorf("%s: this declaration needs ckptctl >= %s (you have %s)", FileName, f.MinReader, ReaderVersion)
	}
	return nil
}

type version struct{ major, minor int }

// parseVersion accepts exactly MAJOR.MINOR, digits only. The release rule is that engine and wheel
// move together, so a two-part number is the whole of it.
func parseVersion(s string) (version, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return version{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return version{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return version{}, false
	}
	return version{major, minor}, true
}

// holds reports whether the declaration says this state was saved.
func (s State) holds(name string) bool {
	for _, e := range stateOrder {
		if e.name == name {
			return e.held(s)
		}
	}
	return false
}

func formats() []string {
	out := []string{FormatDCP, FormatTorchDist, FormatSafetensors, FormatHFTrainer, FormatOther}
	sort.Strings(out)
	return out
}

// Marshal produces the exact bytes the reference writer leaves, so a file that goes in comes out
// identical.
func (f File) Marshal() ([]byte, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Write leaves the declaration in a generation directory, atomically: a half-written declaration is a
// declaration that was never made, and a reader cannot tell a truncated file from a shorter one.
//
// It is the reference writer. Production writers (the adapter, or a library that adopts the schema) use
// the same shape; the verifier reads the bytes back and never trusts what a process said in memory.
func Write(genDir string, f File) error {
	if err := f.validate(); err != nil {
		return err
	}
	raw, err := f.Marshal()
	if err != nil {
		return err
	}
	path := filepath.Join(genDir, FileName)
	tmp, err := os.CreateTemp(genDir, ".resume-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Load reads the declaration of one generation directory. A missing file is not an error: it means the
// writer did not say, and "unstated" is a verdict of its own (decision 0026).
func Load(genDir string) (File, bool, error) {
	raw, err := os.ReadFile(filepath.Join(genDir, FileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return File{}, false, nil
		}
		return File{}, false, err
	}
	f, err := Parse(raw)
	if err != nil {
		return File{}, true, err
	}
	return f, true, nil
}
