package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// TailBytes is how much of the end of a shard the sample check hashes.
//
// A process killed mid-write leaves a short or torn **tail**: the middle of a shard was written minutes
// earlier and is almost never what a kill damages. One mebibyte at the end catches the failure that
// actually happens, for seconds instead of minutes.
const TailBytes = 1 << 20

// Reason is why a copy is in the state it is in. It is the sentence a receipt is built from, so it is a
// closed set rather than free text.
type Reason string

const (
	ReasonOK      Reason = "ok"      // every shard present, sizes and content agree
	ReasonSealed  Reason = "sealed"  // structure agrees; content was not checked
	ReasonMissing Reason = "missing" // no directory at all
	ReasonWaiting Reason = "waiting" // a directory with no manifest, deadline not reached
	ReasonExpired Reason = "expired" // a directory with no manifest, deadline passed
	ReasonStale   Reason = "stale"   // sealed and whole, but older than the generation asked for
	ReasonInvalid Reason = "invalid" // the manifest exists and cannot be believed
	ReasonShort   Reason = "short"   // a shard is missing or smaller than declared
	ReasonHash    Reason = "hash"    // the content does not match what was sealed
)

// Verdict is the whole answer. It never carries a fourth state: `policy.CopyState` has sealed, verified,
// broken and unverified, and inventing a fifth here would mean two vocabularies for one fact.
type Verdict struct {
	State      policy.CopyState
	Reason     Reason
	Detail     string
	Generation int64
	Step       int64
	// SealedAt is the manifest's own claim about when it sealed, carried through so a receipt can show the
	// middle of the three claims. It is never used to decide lateness (see the Options comment).
	SealedAt time.Time
	// Extras are entries in the generation directory that the manifest does not name. They are not a
	// failure — a writer may keep sidecars — but they are the only trace of a second writer in the same
	// directory, and a `verified` verdict is otherwise silent about them (F2 in the rig's findings).
	Extras []string
}

// Options are the two facts from the environment a verdict needs, plus what to spend on content.
type Options struct {
	// Now is our clock. It decides lateness; `sealedAt` from the manifest never does, because it is a
	// claim made by the party we are checking.
	Now time.Time
	// Deadline is the request's notAfter. Zero means "no deadline announced", and then a directory
	// without a manifest stays waiting rather than expiring.
	Deadline time.Time
	// CheckContent hashes shards. Without it the answer can only be `sealed`, never `verified` — and the
	// receipt is required to say which of the two it is.
	CheckContent bool
	// Full hashes every byte. Otherwise only the declared tail hashes are checked, which is cheap and
	// catches the tears a kill actually makes.
	Full bool
}

// Classify reads one generation directory and says what it is.
//
// The checks run in the order that makes the first refusal the answer, the same discipline as
// 0025: missing, waiting, stale, invalid, short, hash. A later check is never asked about a
// copy an earlier one already rejected, which is what keeps the reason meaningful.
func Classify(genDir string, want int64, opts Options) Verdict {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}

	entries, err := os.ReadDir(genDir)
	if err != nil {
		return Verdict{State: policy.CopyUnverified, Reason: ReasonMissing, Detail: err.Error()}
	}

	manifestPath := filepath.Join(genDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		// A directory with no manifest is a write in flight, or a writer that died before sealing. Which
		// one it is depends only on the deadline.
		if errors.Is(err, os.ErrNotExist) && len(entries) > 0 {
			if !opts.Deadline.IsZero() && opts.Now.After(opts.Deadline) {
				return Verdict{State: policy.CopyBroken, Reason: ReasonExpired,
					Detail: fmt.Sprintf("no manifest %s after the deadline", opts.Now.Sub(opts.Deadline).Round(time.Second))}
			}
			return Verdict{State: policy.CopyUnverified, Reason: ReasonWaiting,
				Detail: "no manifest yet: the write is in flight"}
		}
		return Verdict{State: policy.CopyUnverified, Reason: ReasonMissing, Detail: err.Error()}
	}

	m, err := Parse(raw)
	if err != nil {
		return Verdict{State: policy.CopyBroken, Reason: ReasonInvalid, Detail: err.Error()}
	}

	if want > 0 && m.Generation < want {
		return Verdict{State: policy.CopyBroken, Reason: ReasonStale, Generation: m.Generation, Step: m.Step, SealedAt: m.SealedAt,
			Detail: fmt.Sprintf("sealed generation %d, but %d was asked for", m.Generation, want)}
	}

	// Structure first, and entirely before content: a missing shard is a cheaper answer than a hash, and
	// it names the file.
	for _, s := range m.Shards {
		info, err := os.Stat(filepath.Join(genDir, s.Name))
		if err != nil {
			return Verdict{State: policy.CopyBroken, Reason: ReasonShort, Generation: m.Generation, Step: m.Step, SealedAt: m.SealedAt,
				Detail: fmt.Sprintf("shard %s is missing", s.Name)}
		}
		if info.Size() != s.Size {
			return Verdict{State: policy.CopyBroken, Reason: ReasonShort, Generation: m.Generation, Step: m.Step, SealedAt: m.SealedAt,
				Detail: fmt.Sprintf("shard %s is %d bytes, sealed at %d", s.Name, info.Size(), s.Size)}
		}
	}

	if !opts.CheckContent {
		return Verdict{State: policy.CopySealed, Reason: ReasonSealed, Generation: m.Generation, Step: m.Step, SealedAt: m.SealedAt,
			Detail: "structure agrees; content not checked"}
	}

	for _, s := range m.Shards {
		ok, detail, err := checkContent(filepath.Join(genDir, s.Name), s, opts.Full)
		if err != nil {
			return Verdict{State: policy.CopyBroken, Reason: ReasonHash, Generation: m.Generation, Step: m.Step, SealedAt: m.SealedAt,
				Detail: fmt.Sprintf("shard %s cannot be read: %v", s.Name, err)}
		}
		if !ok {
			return Verdict{State: policy.CopyBroken, Reason: ReasonHash, Generation: m.Generation, Step: m.Step, SealedAt: m.SealedAt,
				Detail: fmt.Sprintf("shard %s %s", s.Name, detail)}
		}
	}

	return Verdict{State: policy.CopyVerified, Reason: ReasonOK, Generation: m.Generation, Step: m.Step, SealedAt: m.SealedAt,
		Extras: unlisted(genDir, m), Detail: "content matches the manifest"}
}

// checkContent compares a shard with what the manifest claims about it.
//
// With `full`, every byte is hashed against sha256. Without it, only the declared tail hash is checked —
// and if the writer did not declare one, this cannot fall back to "looks fine": it says so, and the
// caller gets `broken` rather than a `verified` nobody earned.
func checkContent(path string, s Shard, full bool) (bool, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, "", err
	}
	defer f.Close()

	if full {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return false, "", err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != s.SHA256 {
			return false, fmt.Sprintf("hashes to %s, sealed as %s", got[:12], s.SHA256[:12]), nil
		}
		return true, "", nil
	}

	if s.TailSHA256 == "" {
		return false, "declares no tail hash to check without a full read", nil
	}
	n := int64(TailBytes)
	if s.Size < n {
		n = s.Size
	}
	if _, err := f.Seek(s.Size-n, io.SeekStart); err != nil {
		return false, "", err
	}
	h := sha256.New()
	if _, err := io.CopyN(h, f, n); err != nil {
		return false, "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != s.TailSHA256 {
		return false, fmt.Sprintf("tail hashes to %s, sealed as %s", got[:12], s.TailSHA256[:12]), nil
	}
	return true, "", nil
}

// unlisted lists what the generation directory holds that the manifest does not name.
//
// It is reported, not judged. A writer may legitimately keep sidecars; but the manifest is the only index
// this package has, so a second writer's debris is otherwise invisible — and a verified generation can sit
// among files another process is still producing.
func unlisted(genDir string, m Manifest) []string {
	named := make(map[string]bool, len(m.Shards)+2)
	for _, s := range m.Shards {
		named[s.Name] = true
	}
	named["manifest.json"] = true

	entries, err := os.ReadDir(genDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if named[name] || strings.HasSuffix(name, ".tmp") {
			// A temporary file is a write about to be renamed, not content.
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
