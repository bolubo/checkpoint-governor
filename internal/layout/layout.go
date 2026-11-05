// Package layout finds the checkpoints a job left on disk, whoever wrote them.
//
// Before this package every answer started at `gen-*` — our own writer's directories. A job that saved
// through its framework (Hugging Face's `checkpoint-N/`, Lightning's `.ckpt`, a Composer episode) had
// copies on disk that this product could not see, and the receipt said nothing rather than "unknown",
// which reads like "fine" (decision 0026).
//
// The rules are fixed, not guessed:
//
//  1. `gen-*` wins: it is the world we wrote and verified ourselves.
//  2. Otherwise the highest step wins; on a tie `checkpoint-*` > `*.ckpt` > a Composer episode.
//  3. A directory or file with no completion mark and no declaration is not sealed: it is a write in
//     flight, and it is reported as such instead of being counted as the newest copy.
package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bolubo/checkpoint-governor/internal/resume"
)

// Kinds are the layouts this package recognises without being told. A user-declared layout gets KindCustom.
const (
	KindGen       = "gen"
	KindHF        = "hf-checkpoint"
	KindLightning = "lightning-ckpt"
	KindComposer  = "composer-ep"
	KindCustom    = "custom"
)

// Copy is one checkpoint on disk, as the finder sees it. `Sealed` is the whole question: a copy exists
// only when its writer said the write completed — a manifest, a Trainer state file, a non-empty file
// beside its declaration.
type Copy struct {
	// Dir is the directory the declaration lives in (`resume.Load`'s directory).
	Dir string
	// Target is what the declaration describes: "." for the directory itself, or a sibling file name.
	Target string
	// Step orders copies. It comes from the name, else the declaration, else the file's mtime.
	Step int64
	// Kind is one of the constants above.
	Kind string
	// Sealed means the copy finished being written and can be a restore point.
	Sealed bool
	// Declared means a declaration was found beside it (readable or not: an unreadable one is a
	// verdict of its own, not silence).
	Declared bool
}

// Artifact is the name a person recognises: `checkpoint-1000`, `last.ckpt`, or the directory itself.
func (c Copy) Artifact() string {
	if c.Target == "" || c.Target == "." {
		return filepath.Base(c.Dir)
	}
	return c.Target
}

// Load reads this copy's declaration, from the only two places it may live.
func (c Copy) Load() (resume.File, bool, error) {
	path := filepath.Join(c.Dir, "resume.json")
	if c.Target != "" && c.Target != "." {
		path = filepath.Join(c.Dir, c.Target+".resume.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return resume.File{}, false, nil
		}
		return resume.File{}, false, err
	}
	f, err := resume.Parse(raw)
	if err != nil {
		return resume.File{}, true, err
	}
	return f, true, nil
}

// Result is one scan of a directory. `Count` and `Newest` come from the same list on purpose: a receipt
// that says "3 copies, newest is X" must not count with one rule and rank with another.
type Result struct {
	Newest Copy
	Found  bool
	// Sealed is every finished copy, in preference order. A caller that needs "what existed before the
	// job started" cannot answer from Newest alone: the current run may have saved a newer copy since.
	Sealed []Copy
	// Unsealed is the newest thing that exists but is not finished, when nothing sealed exists. Its
	// presence is why the answer is "not sealed" rather than "nothing yet".
	Unsealed *Copy
	Count    int
	// Note records a decision the user should hear: two layouts at the same step, or a custom layout
	// that shadows a built-in one. Empty when nothing was ambiguous.
	Note string
}

// Scan finds every copy under root and answers which one a restore should read.
func Scan(root string, cfg Config) (Result, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, nil
		}
		return Result{}, err
	}

	var sealed, unsealed []Copy
	seen := map[string]bool{} // "dir|target" so a custom glob cannot double-count a built-in
	for _, e := range entries {
		for _, c := range builtin(root, e) {
			key := c.Dir + "|" + c.Target
			if seen[key] {
				continue
			}
			seen[key] = true
			if c.Declared {
				if f, _, err := c.Load(); err == nil {
					c.Step = f.Step
				}
			}
			if c.Sealed {
				sealed = append(sealed, c)
			} else {
				unsealed = append(unsealed, c)
			}
		}
	}

	for _, c := range custom(root, entries, cfg) {
		key := c.Dir + "|" + c.Target
		if seen[key] {
			continue
		}
		seen[key] = true
		if c.Declared {
			if f, _, err := c.Load(); err == nil {
				c.Step = f.Step
			}
		}
		if c.Sealed {
			sealed = append(sealed, c)
		} else {
			unsealed = append(unsealed, c)
		}
	}

	res := Result{Count: len(sealed), Sealed: sealed}
	if len(sealed) > 0 {
		// Ascending by `better`, so the preferred copy is first.
		sort.Slice(sealed, func(i, j int) bool { return better(sealed[i], sealed[j]) })
		res.Newest, res.Found = sealed[0], true
		if note := ambiguity(sealed); note != "" {
			res.Note = note
		}
	}
	if len(unsealed) > 0 {
		newest := unsealed[0]
		for _, c := range unsealed[1:] {
			if c.Step > newest.Step {
				newest = c
			}
		}
		res.Unsealed = &newest
	}
	return res, nil
}

// Newest is `Scan` when the caller only needs the copy a restore should read.
func Newest(root string, cfg Config) (Copy, bool, error) {
	res, err := Scan(root, cfg)
	if err != nil {
		return Copy{}, false, err
	}
	return res.Newest, res.Found, nil
}

// better reports whether a should be preferred over b. Ordering is the contract, so it is one place.
func better(a, b Copy) bool {
	if (a.Kind == KindGen) != (b.Kind == KindGen) {
		return a.Kind == KindGen
	}
	if a.Step != b.Step {
		return a.Step > b.Step
	}
	return rank(a.Kind) < rank(b.Kind)
}

func rank(kind string) int {
	switch kind {
	case KindGen:
		return 0
	case KindHF:
		return 1
	case KindLightning:
		return 2
	case KindComposer:
		return 3
	default:
		return 4
	}
}

// ambiguity names two different layouts that claim the same step. It is a Note, not a refusal: the
// priority is fixed, so the answer is right, but a person should know a second copy sits beside it.
func ambiguity(copies []Copy) string {
	best := copies[0]
	var others []string
	for _, c := range copies[1:] {
		if c.Step == best.Step && c.Kind != best.Kind {
			others = append(others, c.Artifact())
		}
	}
	if len(others) == 0 {
		return ""
	}
	return fmt.Sprintf("step %d exists as %s and %s; judged %s",
		best.Step, strings.Join(others, ", "), best.Artifact(), best.Kind)
}

// builtin recognises one directory entry, if it is a layout we know.
func builtin(root string, e os.DirEntry) []Copy {
	name := e.Name()

	// Our own world: a directory with a manifest. `gen-N` is the contract's writer (0025); `step-N` is
	// the older writer, which the same product still has to count and retire.
	for _, prefix := range []string{resume.GenerationPrefix, "step-"} {
		if !e.IsDir() || !strings.HasPrefix(name, prefix) {
			continue
		}
		n, ok := parseTrailing(name, prefix)
		if !ok {
			return nil
		}
		dir := filepath.Join(root, name)
		_, manifestErr := os.Stat(filepath.Join(dir, "manifest.json"))
		_, declErr := os.Stat(filepath.Join(dir, "resume.json"))
		return []Copy{{
			Dir: dir, Target: ".", Step: n, Kind: KindGen,
			Sealed: manifestErr == nil, Declared: declErr == nil,
		}}
	}

	// Hugging Face: checkpoint-<step>/ with the Trainer's own state file.
	if e.IsDir() && strings.HasPrefix(name, "checkpoint-") {
		n, ok := parseTrailing(name, "checkpoint-")
		if !ok {
			return nil
		}
		dir := filepath.Join(root, name)
		_, stateErr := os.Stat(filepath.Join(dir, "trainer_state.json"))
		_, declErr := os.Stat(filepath.Join(dir, "resume.json"))
		sealed := stateErr == nil || declErr == nil
		return []Copy{{
			Dir: dir, Target: ".", Step: n, Kind: KindHF,
			Sealed: sealed, Declared: declErr == nil,
		}}
	}

	// Lightning: a non-empty .ckpt file; its declaration is `<file>.resume.json` beside it.
	if !e.IsDir() && strings.HasSuffix(name, ".ckpt") {
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if resolved, err := os.Stat(path); err == nil {
				info = resolved
			}
		}
		_, declErr := os.Stat(path + ".resume.json")
		if info.Size() <= 0 && declErr != nil {
			return nil // a zero-byte file is a write that has not started
		}
		return []Copy{{
			Dir: root, Target: name, Step: info.ModTime().Unix(), Kind: KindLightning,
			Sealed: info.Size() > 0, Declared: declErr == nil,
		}}
	}

	// A declaration that describes a sibling file (Composer, or a custom artifact) is its own entry.
	if !e.IsDir() && strings.HasSuffix(name, ".resume.json") {
		target := strings.TrimSuffix(name, ".resume.json")
		if target == "" || target == "resume" {
			return nil
		}
		path := filepath.Join(root, target)
		info, err := os.Stat(path)
		if err != nil {
			return nil // a declaration without its artifact is not a copy
		}
		kind := KindComposer
		if strings.HasSuffix(target, ".ckpt") {
			kind = KindLightning
		}
		step := info.ModTime().Unix()
		if f, _, err := (Copy{Dir: root, Target: target}).Load(); err == nil {
			step = f.Step
		}
		return []Copy{{
			Dir: root, Target: target, Step: step, Kind: kind,
			Sealed: true, Declared: true,
		}}
	}

	// Composer episodes: present, and sealed only when its declaration sits beside it.
	if !e.IsDir() && strings.Contains(name, "ep") && strings.Contains(name, "rank") {
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			return nil
		}
		_, declErr := os.Stat(path + ".resume.json")
		return []Copy{{
			Dir: root, Target: name, Step: info.ModTime().Unix(), Kind: KindComposer,
			Sealed: declErr == nil, Declared: declErr == nil,
		}}
	}

	return nil
}

// custom recognises whatever the user's `layout.json` glob matches. It never overrides a built-in copy
// with the same identity (dir + target); it only adds copies the built-ins could not see.
func custom(root string, entries []os.DirEntry, cfg Config) []Copy {
	if cfg.Glob == "" {
		return nil
	}
	var out []Copy
	for _, e := range entries {
		ok, err := filepath.Match(cfg.Glob, e.Name())
		if err != nil || !ok {
			continue
		}
		name := e.Name()
		path := filepath.Join(root, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		isDir := e.IsDir()
		if isDir {
			info, err = os.Stat(path)
			if err != nil {
				continue
			}
		}
		step, ok := stepFrom(name, cfg.StepRegex)
		if !ok {
			step = info.ModTime().Unix()
		}
		declPath := filepath.Join(path, cfg.declaration())
		if !isDir {
			declPath = path + ".resume.json"
		}
		_, declErr := os.Stat(declPath)
		declared := declErr == nil
		sealed := false
		switch {
		case isDir && declared:
			sealed = true
		case !isDir && info.Size() > 0 && declared:
			sealed = true
		case !isDir && info.Size() > 0 && cfg.ProbeCmd != "":
			sealed = true // the probe is the completion mark when the writer leaves nothing else
		}
		dir, target := path, "."
		if !isDir {
			dir, target = root, name
		}
		// A declaration's own step is more trustworthy than a name we had to parse.
		if declared {
			if f, _, err := (Copy{Dir: dir, Target: target}).Load(); err == nil {
				step = f.Step
			}
		}
		out = append(out, Copy{
			Dir: dir, Target: target, Step: step, Kind: KindCustom,
			Sealed: sealed, Declared: declared,
		})
	}
	return out
}

func (c Config) declaration() string {
	if c.Declaration != "" {
		return c.Declaration
	}
	return "resume.json"
}

// parseTrailing reads the number a name ends with, after prefix.
func parseTrailing(name, prefix string) (int64, bool) {
	rest := strings.TrimPrefix(name, prefix)
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// stepFrom applies the user's regex, returning the first capture group as the step.
func stepFrom(name, pattern string) (int64, bool) {
	if pattern == "" {
		return 0, false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, false
	}
	m := re.FindStringSubmatch(name)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
