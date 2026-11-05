// Package evidence assembles the facts a person or an agent needs in one place: what the directory
// is doing, whether it can be resumed, whether the newest copy is whole, what was attempted, and
// what the volume promises.
//
// It is a read-only view over the packages that already decide those questions. It does not re-judge
// anything: a bundle that could disagree with `status`, `resumable` or `verify` would be a second
// answer to one question, and the whole product is built on there being one.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/layout"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/resume"
	"github.com/bolubo/checkpoint-governor/internal/storage"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// Schema is the bundle's version. 1 is frozen for this release: fields may be added, never removed or
// renamed, because a script or an agent reads this without a release note.
const Schema = 1

// Bundle is one directory's evidence.
type Bundle struct {
	Schema      int       `json:"schema"`
	GeneratedAt time.Time `json:"generatedAt"`
	Dir         string    `json:"dir"`
	Unit        string    `json:"unitId,omitempty"`
	Status      Status    `json:"status"`
	Resume      Resume    `json:"resume"`
	Newest      *Newest   `json:"newest,omitempty"`
	Attempts    []Attempt `json:"attempts,omitempty"`
	Storage     *Storage  `json:"storage,omitempty"`
	Problems    []Problem `json:"problems"`
}

// Status is the controller's ledger, reduced to the fields a reader needs.
type Status struct {
	Generation int64  `json:"generation"`
	Step       int64  `json:"step"`
	Phase      string `json:"phase,omitempty"`
	SealedAt   string `json:"sealedAt,omitempty"`
	Writing    bool   `json:"writing"`
}

// Resume is the verdict `resumable` prints, with a state word for machines.
type Resume struct {
	State       string   `json:"state"` // resumable | not resumable | unknown
	Reason      string   `json:"reason,omitempty"`
	Detail      string   `json:"detail,omitempty"`
	Line        string   `json:"line,omitempty"`
	Generation  int64    `json:"generation,omitempty"`
	Step        int64    `json:"step,omitempty"`
	World       World    `json:"world"`
	Missing     []string `json:"missing,omitempty"`
	MissingRank int      `json:"missingRank,omitempty"`
}

// World is the topology a copy was written under.
type World struct {
	Ranks       int  `json:"ranks"`
	TP          int  `json:"tp"`
	PP          int  `json:"pp"`
	DP          int  `json:"dp"`
	Reshardable bool `json:"reshardable"`
}

// Newest is the newest sealed generation and the verdict on its bytes.
type Newest struct {
	Generation int64   `json:"generation"`
	Step       int64   `json:"step"`
	SealedAt   string  `json:"sealedAt,omitempty"`
	State      string  `json:"state"`
	Reason     string  `json:"reason,omitempty"`
	Detail     string  `json:"detail,omitempty"`
	Verified   bool    `json:"verified"`
	Shards     []Shard `json:"shards,omitempty"`
}

// Shard says what the volume holds for one manifest entry.
type Shard struct {
	Name    string `json:"name"`
	Member  int    `json:"member"`
	Size    int64  `json:"size"`
	Present bool   `json:"present"`
	TailOK  bool   `json:"tailOk"`
}

// Attempt is one write attempt from the ledger.
type Attempt struct {
	ID        string    `json:"id"`
	Trigger   string    `json:"trigger,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

// Storage is what the volume promises, as measured by the storage probe.
type Storage struct {
	Class   string `json:"class"`
	Promise string `json:"promise,omitempty"`
	FS      string `json:"fs,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Problem is a closed-code refusal with the sentence a person acts on.
type Problem struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
}

// Collect reads one directory. Every error is carried as a problem or an "unknown" field rather than
// returned: a bundle that refuses to assemble itself because one probe failed is less useful than one
// that says which probe failed.
func Collect(dir string) Bundle {
	now := time.Now()
	b := Bundle{Schema: Schema, GeneratedAt: now, Dir: dir, Problems: []Problem{}}

	if l, err := advise.Load(dir); err == nil {
		b.Unit = l.Status.UnitID
		b.Status = Status{
			Generation: l.Status.Generation,
			Step:       l.Status.SealStep,
			Phase:      string(l.Status.Phase),
			SealedAt:   unixTime(l.Status.SealAt),
			Writing:    l.Status.Phase == advise.PhaseWriting,
		}
		for _, a := range l.Status.Attempts {
			b.Attempts = append(b.Attempts, Attempt{
				ID: a.ID, Trigger: a.Trigger, Outcome: a.Outcome, StartedAt: a.StartedAt,
			})
		}
	}

	collectResume(&b, dir)
	collectNewest(&b, dir, now)
	collectStorage(&b, dir)
	dedupeProblems(&b)
	return b
}

func collectResume(b *Bundle, dir string) {
	cfg, _, err := layout.LoadConfig(dir)
	if err != nil {
		b.Resume = Resume{State: "unknown", Detail: err.Error()}
		return
	}
	res, err := layout.Scan(dir, cfg)
	if err != nil {
		b.Resume = Resume{State: "unknown", Detail: err.Error()}
		return
	}
	if !res.Found && res.Unsealed == nil {
		b.Resume = Resume{State: "unknown", Detail: "nothing sealed yet"}
		return
	}
	v := res.Verdict(cfg, resume.World{})
	b.Resume = Resume{
		State:      resumeState(v),
		Reason:     string(v.Reason),
		Detail:     v.Detail,
		Line:       v.Line(),
		Generation: v.Generation,
		Step:       v.Step,
		World: World{
			Ranks: v.World.Ranks, TP: v.World.TP, PP: v.World.PP, DP: v.World.DP,
			Reshardable: v.World.Reshardable,
		},
		Missing:     v.Missing,
		MissingRank: v.MissingRank,
	}
	if b.Resume.State == "not resumable" {
		addProblem(b, resumeProblemCode(v), v.Line())
	}
}

func resumeState(v resume.Verdict) string {
	switch {
	case v.Resumable():
		return "resumable"
	case v.Reason == resume.ReasonUnstated || v.Reason == resume.ReasonInvalid:
		return "unknown"
	default:
		return "not resumable"
	}
}

func collectNewest(b *Bundle, dir string, now time.Time) {
	sealed, ok, err := resume.NewestSealed(dir)
	if err != nil || !ok {
		return
	}
	m := sealed.Manifest
	v := verify.Classify(sealed.Dir, 0, verify.Options{Now: now, CheckContent: true})
	n := &Newest{
		Generation: m.Generation, Step: m.Step,
		SealedAt: m.SealedAt.UTC().Format(time.RFC3339),
		State:    string(v.State), Reason: string(v.Reason), Detail: v.Detail,
		Verified: v.State == policy.CopyVerified,
		Shards:   describeShards(sealed.Dir, m.Shards),
	}
	b.Newest = n
	if b.Status.Generation == 0 {
		b.Status.Generation = m.Generation
		b.Status.Step = m.Step
		if b.Status.SealedAt == "" {
			b.Status.SealedAt = n.SealedAt
		}
	}
	if v.State == policy.CopyBroken {
		addProblem(b, verifyProblemCode(v), v.Detail)
	}
}

func describeShards(genDir string, shards []verify.Shard) []Shard {
	out := make([]Shard, 0, len(shards))
	for _, s := range shards {
		d := Shard{Name: s.Name, Member: s.Member, Size: s.Size}
		if _, err := os.Stat(filepath.Join(genDir, s.Name)); err == nil {
			d.Present = true
			d.TailOK = tailMatches(filepath.Join(genDir, s.Name), s)
		}
		out = append(out, d)
	}
	return out
}

func tailMatches(path string, s verify.Shard) bool {
	if s.TailSHA256 == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	n := int64(verify.TailBytes)
	if s.Size < n {
		n = s.Size
	}
	if n <= 0 {
		return false
	}
	if _, err := f.Seek(s.Size-n, io.SeekStart); err != nil {
		return false
	}
	h := sha256.New()
	if _, err := io.CopyN(h, f, n); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == s.TailSHA256
}

func collectStorage(b *Bundle, dir string) {
	r, err := storage.Probe(dir)
	if err != nil {
		b.Storage = &Storage{Class: "unknown", Detail: err.Error()}
		return
	}
	b.Storage = &Storage{Class: r.Class, Promise: r.Promise, FS: r.FSName}
	if !r.Safe() {
		addProblem(b, "storage_unsafe", r.Promise)
	}
}

func verifyProblemCode(v verify.Verdict) string {
	switch v.Reason {
	case verify.ReasonShort:
		if strings.Contains(v.Detail, "missing") {
			return "shard_missing"
		}
		return "shard_short"
	case verify.ReasonHash:
		return "tail_mismatch"
	case verify.ReasonInvalid:
		return "manifest_invalid"
	case verify.ReasonWaiting, verify.ReasonExpired, verify.ReasonMissing:
		return "unsealed"
	default:
		return "unsealed"
	}
}

func resumeProblemCode(v resume.Verdict) string {
	switch v.Reason {
	case resume.ReasonShardMissing:
		return "shard_missing"
	case resume.ReasonWorldChanged:
		return "world_mismatch"
	default:
		return "declaration_invalid"
	}
}

func addProblem(b *Bundle, code, detail string) {
	if code == "" {
		return
	}
	b.Problems = append(b.Problems, Problem{Code: code, Detail: detail, Remedy: remedyFor(code)})
}

func dedupeProblems(b *Bundle) {
	seen := map[string]bool{}
	out := b.Problems[:0]
	for _, p := range b.Problems {
		if seen[p.Code] {
			continue
		}
		seen[p.Code] = true
		out = append(out, p)
	}
	b.Problems = out
}

func remedyFor(code string) string {
	switch code {
	case "shard_missing", "shard_short", "tail_mismatch":
		return "rewrite the shard and seal the generation again"
	case "manifest_invalid", "unsealed":
		return "the writer must seal a valid manifest; a directory without one is a write in flight"
	case "declaration_invalid":
		return "write a resume.json that declares what the restore needs"
	case "world_mismatch":
		return "resume under the world the checkpoint was written with"
	case "storage_unsafe":
		return "use a shared volume that passes `doctor --storage`, or do not resume across pods from it"
	default:
		return ""
	}
}

func unixTime(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}
