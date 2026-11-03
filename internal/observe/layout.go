package observe

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ManifestName is the file written last: its presence is the seal.
//
// The layout is named in design ("The layout it reads"). Nothing else is promised about a
// checkpoint directory: a directory without this file is a write that has not finished, and a
// manifest that does not match its shards is a broken checkpoint, not an old format.
const ManifestName = "manifest.json"

// manifest is the part of a checkpoint the controller is allowed to trust.
//
// The digest is carried but not checked here: reading every byte is an O(bytes) operation and
// belongs to the background verification loop. The structure check is O(shards) and is what makes
// "sealed" mean something on the hot path.
type manifest struct {
	Generation int64           `json:"generation"`
	Step       int64           `json:"step"`
	Shards     []manifestShard `json:"shards"`
}

// manifestShard is one row of the manifest. The documented contract names the file and its size
// (decision 0025); the older writer used `rank`/`bytes` and implied `rank-<n>.bin`. One verifier reads
// both, because both dialects exist in this repository's history.
type manifestShard struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`

	Rank  *int  `json:"rank"`
	Bytes int64 `json:"bytes"`
}

// fileName is the shard's path inside the copy: the manifest's own name when it has one, and the
// rank it names otherwise. A reader that reconstructs `rank-<n>.bin` from an index cannot read a writer
// that names its shards anything else — the measured case was `rank-0.pt`.
func (s manifestShard) fileName(index int) string {
	if s.Name != "" {
		return s.Name
	}
	rank := index
	if s.Rank != nil {
		rank = *s.Rank
	}
	return shardName(rank)
}

// length is the shard's size in bytes, in either dialect.
func (s manifestShard) length() int64 {
	if s.Name != "" {
		return s.Size
	}
	return s.Bytes
}

// stepDir is one `step-<N>` or `gen-<n>` directory as read from disk.
type stepDir struct {
	name string
	step int64
	// generation says the directory is named by generation, not by step: its step comes from the
	// manifest, because the directory's number is not a step at all.
	generation bool
	bytes      int64
	sealed     bool
	// changedAt is the newest modification inside the directory. It answers "is this write still
	// moving", which total elapsed time cannot: a large checkpoint on a busy disk is slow, not dead.
	changedAt time.Time
	// reason says why it is not sealed. It is for the log, never for a decision: the policy only
	// sees the state.
	reason string
	// code is the structured form of reason; empty means "no manifest yet", which is not a failure.
	code string
	// reads and stats are the system calls this directory cost, counted where they happen. They are
	// the basis of the governor's own overhead: a number estimated from the code would drift the
	// moment the code changes.
	reads, stats int
}

// readStepDir reads one directory and decides whether it is a sealed checkpoint.
//
// The order matters: the manifest first (no manifest = not sealed yet), then its agreement with the
// directory name, then every shard's size. A checkpoint that lists a shard it does not have is
// worse than no checkpoint, because a reader would trust the manifest.
func readStepDir(root, name string, cache *manifestCache) stepDir {
	step, ok := parseStepName(name)
	if !ok {
		return stepDir{name: name, reason: "the name is not step-<N> or gen-<n>"}
	}
	gen := strings.HasPrefix(name, "gen-")
	d := stepDir{name: name, step: step, generation: gen, changedAt: newestChange(filepath.Join(root, name))}

	// One open, one fstat, one read: the manifest's time is needed for the stale-seal rule, and a
	// second stat for it would be a system call spent on nothing.
	mf, err := os.Open(filepath.Join(root, name, ManifestName))
	if err != nil {
		if os.IsNotExist(err) {
			d.reason = "no manifest: the write has not sealed"
		} else {
			d.reason = "the manifest cannot be read: " + err.Error()
			d.code = FailManifest
		}
		return d
	}
	manifestInfo, err := mf.Stat()
	if err != nil {
		mf.Close()
		d.reason = "the manifest cannot be read: " + err.Error()
		d.code = FailManifest
		return d
	}
	d.stats = 1

	var m manifest
	if parsed, hit := cache.get(filepath.Join(root, name, ManifestName), manifestInfo); hit {
		// Same file, same bytes: skip the read. The stat above already told us it did not move.
		m = parsed
		mf.Close()
	} else {
		raw, err := io.ReadAll(mf)
		mf.Close()
		d.reads = 1
		if err != nil {
			d.reason = "the manifest cannot be read: " + err.Error()
			d.code = FailManifest
			return d
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			d.reason = "the manifest is not valid JSON: " + err.Error()
			d.code = FailManifest
			return d
		}
		cache.put(filepath.Join(root, name, ManifestName), manifestInfo, m)
	}

	// A `step-<N>` directory promises its step in its name, so a manifest that disagrees is broken.
	// A `gen-<n>` directory is named by generation — the number is not a step — so the manifest is the
	// only place its step can come from. Comparing the two called every generation-layout copy broken
	// (measured: `gen-0001` holding step 121 was read as "step 1", and the engine asked for a repair
	// twelve seconds after a clean seal).
	if gen {
		if m.Step <= 0 {
			d.reason = "the manifest names no step"
			d.code = FailManifest
			return d
		}
		d.step = m.Step
	} else if m.Step != step {
		d.reason = fmt.Sprintf("the manifest says step %d and the directory says %d", m.Step, step)
		d.code = FailStepName
		return d
	}
	if len(m.Shards) == 0 {
		d.reason = "the manifest lists no shards"
		d.code = FailManifest
		return d
	}

	var total int64
	for i, s := range m.Shards {
		file := s.fileName(i)
		path := filepath.Join(root, name, file)
		d.stats++
		info, err := os.Stat(path)
		if err != nil {
			d.reason = file + " is missing"
			d.code = FailShardGone
			return d
		}
		if info.Size() != s.length() {
			d.reason = fmt.Sprintf("%s is %d bytes and the manifest says %d", file, info.Size(), s.length())
			d.code = FailShardShort
			return d
		}
		// A shard newer than the manifest means the directory was sealed and then rewritten in
		// place. Sizes can match while the bytes belong to two different attempts, so this cannot be
		// caught by counting bytes — only by the order the writer left behind.
		if info.ModTime().After(manifestInfo.ModTime()) {
			d.reason = fmt.Sprintf("%s changed after the manifest was sealed", file)
			d.code = FailSealStale
			return d
		}
		total += s.length()
	}

	d.sealed = true
	d.bytes = total
	return d
}

func shardName(rank int) string { return "rank-" + strconv.Itoa(rank) + ".bin" }

// newestChange is the latest modification time of the directory or of any file directly inside it.
//
// Shards live directly in the step directory, so a shallow read is enough, and it keeps the cost at
// one stat per file — the same order as the seal check that already runs every round.
func newestChange(dir string) time.Time {
	var newest time.Time
	if info, err := os.Stat(dir); err == nil {
		newest = info.ModTime()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return newest
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// parseStepName accepts `step-<n>` and `gen-<n>`, both non-negative.
//
// Two prefixes because the contract's writer (0025) names its directories by generation, and the older one
// counted steps. Recognising only the first made every checkpoint written under the new contract invisible
// to this package — the governor then said "no writes" immediately after a write (F11 in the rig's findings).
func parseStepName(name string) (int64, bool) {
	rest, ok := strings.CutPrefix(name, "step-")
	if !ok {
		rest, ok = strings.CutPrefix(name, "gen-")
	}
	if !ok || rest == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
