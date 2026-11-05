package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// The gate reads the same directories the controller writes, and before it releases an arrival it
// reads the newest copy back: a manifest that exists is not the same fact as a copy that is whole.

func readDir(dir string) ([]os.DirEntry, error) { return os.ReadDir(dir) }

func statManifest(dir, name string) (os.FileInfo, error) {
	return os.Stat(filepath.Join(dir, name, "manifest.json"))
}

func dirPrefix(name string) bool {
	return strings.HasPrefix(name, "gen-") || strings.HasPrefix(name, "step-")
}

// stepOfCheckpoint names the step a directory holds. `step-N` says it; a generation directory carries
// the step in its manifest, because the directory name is the generation (0025).
func stepOfCheckpoint(dir, name string) int64 {
	if rest, ok := strings.CutPrefix(name, "step-"); ok {
		n, _ := strconv.ParseInt(rest, 10, 64)
		return n
	}
	raw, err := os.ReadFile(filepath.Join(dir, name, "manifest.json"))
	if err != nil {
		return 0
	}
	var m struct {
		Step int64 `json:"step"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0
	}
	return m.Step
}

// findUnitDir locates one unit's checkpoint directory under --ckpt-root. The unit id is the identity
// the controller publishes in state.json, so that file is the authority; the directory's basename is
// tried first because it is the cheapest and the common case.
func findUnitDir(root, unitID string) (string, bool) {
	if root == "" || unitID == "" {
		return "", false
	}
	if base := unitID[strings.LastIndex(unitID, "/")+1:]; base != "" {
		if d := filepath.Join(root, base); hasState(d) {
			return d, true
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(root, e.Name())
		if unitOf(d) == unitID {
			return d, true
		}
	}
	return "", false
}

func hasState(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".ckpt-governor", "state.json"))
	return err == nil
}

// rankShard finds the member a shard name declares: rank-00.bin, rank0.pt and rank_1 are one rank.
// The separator after `rank` is optional because writers spell it both ways.
var rankShard = regexp.MustCompile(`rank[-_.]?([0-9]+)`)

// declaredRanks is which ranks a manifest names. A writer that does not name its shards this way
// leaves the map empty, and the gate then checks presence only — it must not invent a member the
// manifest never claimed.
func declaredRanks(genDir string) map[int]bool {
	raw, err := os.ReadFile(filepath.Join(genDir, "manifest.json"))
	if err != nil {
		return nil
	}
	var m struct {
		Shards []struct {
			Name string `json:"name"`
		} `json:"shards"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := map[int]bool{}
	for _, s := range m.Shards {
		if match := rankShard.FindStringSubmatch(s.Name); match != nil {
			if n, err := strconv.Atoi(match[1]); err == nil {
				out[n] = true
			}
		}
	}
	return out
}

// missingRank is the first rank a workload of `want` members did not leave a shard for. A workload
// of one pod cannot be missing a member; a writer that declares no rank names cannot be judged this
// way, and the manifest's own shard list is the whole check.
func missingRank(ranks map[int]bool, want int) (int, bool) {
	if want <= 1 || len(ranks) == 0 {
		return 0, false
	}
	for i := 0; i < want; i++ {
		if !ranks[i] {
			return i, true
		}
	}
	return 0, false
}

// newestVerifiedSeal is the only seal the gate releases on: a manifest that appeared after the hold,
// whose shards read back, whose sealedAt is not older than the hold, and which names every rank the
// workload has. A manifest on its own proves a writer reached the seal; it does not prove the copy is
// whole, and releasing an arrival for a copy that cannot be restored is worse than not releasing
// (0027).
//
// A manifest whose shape cannot be verified (no names, no hashes, no seal time) is refused for the
// same reason: the gate cannot tell whether it is whole, and "probably whole" is not a promise.
func newestVerifiedSeal(dir string, since time.Time, wantRanks int) (int64, bool, string) {
	entries, err := readDir(dir)
	if err != nil {
		return 0, false, "cannot read the directory: " + err.Error()
	}
	var newest time.Time
	var name string
	for _, e := range entries {
		if !e.IsDir() || !dirPrefix(e.Name()) {
			continue
		}
		info, err := statManifest(dir, e.Name())
		if err != nil || info.ModTime().Before(since) {
			continue
		}
		if name == "" || info.ModTime().After(newest) {
			newest, name = info.ModTime(), e.Name()
		}
	}
	if name == "" {
		return 0, false, ""
	}

	genDir := filepath.Join(dir, name)
	v := verify.Classify(genDir, 0, verify.Options{Now: time.Now(), CheckContent: true})
	if v.State != policy.CopyVerified {
		problem := string(v.Reason)
		if v.Detail != "" {
			problem += ": " + v.Detail
		}
		return 0, false, problem
	}
	// Writers truncate `sealedAt` to a whole second, so the comparison has the same resolution. The
	// manifest's mtime above is the finer guard against a copy that was already there.
	if v.SealedAt.Before(since.Truncate(time.Second)) {
		return 0, false, fmt.Sprintf("sealed at %s, before the hold at %s",
			v.SealedAt.UTC().Format("15:04:05Z"), since.UTC().Format("15:04:05Z"))
	}
	if rank, missing := missingRank(declaredRanks(genDir), wantRanks); missing {
		return 0, false, fmt.Sprintf("the seal names %d of %d ranks; rank %d has no shard",
			len(declaredRanks(genDir)), wantRanks, rank)
	}
	return v.Step, true, ""
}

// waitForVerifiedSeals polls until every workload has a copy sealed after the hold that reads back
// whole, or the deadline passes. Problems are printed once per change, because a wait that silently
// fails is indistinguishable from a hang.
func waitForVerifiedSeals(workloads []workloadVictim, dirs map[string]string, since time.Time, deadline time.Duration, out io.Writer) (int64, bool) {
	end := since.Add(deadline)
	last := map[string]string{}
	var step int64
	for {
		done := true
		for _, w := range workloads {
			s, ok, problem := newestVerifiedSeal(dirs[w.ID], since, len(w.Pods))
			if ok {
				if s > step {
					step = s
				}
				continue
			}
			done = false
			if problem != "" && problem != last[w.ID] {
				fmt.Fprintf(out, "gate       %s: not verified — %s\n", w.ID, problem)
				last[w.ID] = problem
			}
		}
		if done && len(workloads) > 0 {
			return step, true
		}
		if time.Now().After(end) {
			return 0, false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// unitOf reads the unit id from the published plan. A directory without one is not a unit, and a
// directory whose plan cannot be read is not matched by guessing.
func unitOf(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, ".ckpt-governor", "state.json"))
	if err != nil {
		return ""
	}
	var s struct {
		UnitID string `json:"unitId"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s.UnitID
}
