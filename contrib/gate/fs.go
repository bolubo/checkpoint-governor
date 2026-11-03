package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The gate reads the same directories the controller writes. It never reads a checkpoint's contents:
// a manifest's existence and time is the whole proof it needs.

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
