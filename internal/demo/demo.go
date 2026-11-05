// Package demo builds a small, deterministic checkpoint and then breaks it in one of the ways a
// killed job actually breaks one. It exists so a person can see the contract in thirty seconds,
// without a cluster, a GPU, or a training loop: write two shards, seal, delete a shard, ask
// `resumable`, repair, ask again.
//
// The demo never invents a verdict. The files it writes are the same shape a real writer leaves, and
// the commands that judge them are the real ones; this package only produces the fixture and the
// fault.
package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// MarkerName is the file that says "this directory belongs to the demo". Without it, Create and
// Clean refuse to touch a directory: a demo command that can delete a real checkpoint is not a
// demo, it is an accident.
const MarkerName = ".ckpt-demo"

// Scenario is which fault the fixture is given.
type Scenario string

const (
	ScenarioKill  Scenario = "kill"  // rank1.pt is gone: the kill landed between the shards and the manifest
	ScenarioTorn  Scenario = "torn"  // the last bytes of rank1.pt changed, size intact
	ScenarioShort Scenario = "short" // rank1.pt is shorter than the manifest says
	ScenarioClean Scenario = "clean" // no fault
)

// Generation and Step are the fixture's numbers. They are fixed so the demo's output can be golden.
const (
	Generation = 1
	Step       = 300
)

// Fixture is what the demo created, and the sentence describing the fault.
type Fixture struct {
	Dir      string
	GenDir   string
	Scenario Scenario
	Fault    string
	Shards   []string
}

// ParseScenario validates a --scenario value.
func ParseScenario(s string) (Scenario, error) {
	switch Scenario(s) {
	case ScenarioKill, ScenarioTorn, ScenarioShort, ScenarioClean:
		return Scenario(s), nil
	default:
		return "", fmt.Errorf("unknown scenario %q (want kill, torn, short or clean)", s)
	}
}

type marker struct {
	Schema int   `json:"schema"`
	Gen    int64 `json:"generation"`
	Step   int64 `json:"step"`
}

// Create writes the fixture into dir and applies the scenario's fault.
//
// It refuses a non-empty directory it did not create: "demo --dir=/ckpt/prod" must not overwrite a
// real checkpoint, and a marker is the only thing that lets a later --repair or --clean know the
// directory is ours.
func Create(dir string, sc Scenario) (Fixture, error) {
	if err := ownedOrEmpty(dir); err != nil {
		return Fixture{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Fixture{}, err
	}
	f, err := writeFixture(dir, Generation, Step)
	if err != nil {
		return Fixture{}, err
	}
	f.Scenario = sc
	f.Fault = faultSentence(sc)
	if err := applyFault(f.GenDir, sc); err != nil {
		return Fixture{}, err
	}
	return f, nil
}

// Repair rewrites both shards and seals the generation again. It refuses a directory without the
// demo marker, for the same reason Clean does.
func Repair(dir string) (Fixture, error) {
	m, err := readMarker(dir)
	if err != nil {
		return Fixture{}, err
	}
	f, err := writeFixture(dir, m.Gen, m.Step)
	if err != nil {
		return Fixture{}, err
	}
	f.Scenario = ScenarioClean
	f.Fault = "none — the shard was rewritten and the generation sealed again"
	return f, nil
}

// Clean removes a demo directory. It refuses one it did not create.
func Clean(dir string) error {
	if _, err := readMarker(dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func ownedOrEmpty(dir string) error {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if _, err := readMarker(dir); err == nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty and is not a demo directory: refusing to write into it", dir)
	}
	return nil
}

func readMarker(dir string) (marker, error) {
	var m marker
	raw, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, fmt.Errorf("%s is not a demo directory (no %s); refusing to change it", dir, MarkerName)
		}
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("%s: %w", MarkerName, err)
	}
	return m, nil
}

func writeFixture(dir string, gen, step int64) (Fixture, error) {
	genDir := filepath.Join(dir, fmt.Sprintf("gen-%04d", gen))
	if err := os.RemoveAll(genDir); err != nil {
		return Fixture{}, err
	}
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		return Fixture{}, err
	}

	names := []string{"rank0.pt", "rank1.pt"}
	for rank, name := range names {
		if err := os.WriteFile(filepath.Join(genDir, name), shardData(rank, step), 0o644); err != nil {
			return Fixture{}, err
		}
	}
	m, err := buildManifest(genDir, gen, step, names)
	if err != nil {
		return Fixture{}, err
	}
	if err := writeManifest(genDir, m); err != nil {
		return Fixture{}, err
	}
	if err := writeResume(genDir, gen, step); err != nil {
		return Fixture{}, err
	}
	if err := writeAtomic(filepath.Join(dir, MarkerName), marker{
		Schema: 1, Gen: gen, Step: step,
	}); err != nil {
		return Fixture{}, err
	}
	return Fixture{Dir: dir, GenDir: genDir, Shards: names}, nil
}

// shardData is deterministic on purpose: Repair rewrites exactly the bytes the manifest was built
// from, so a repaired copy is byte-identical to a clean one.
func shardData(rank int, step int64) []byte {
	return []byte(fmt.Sprintf("rank %d, step %d, demo payload 0123456789abcdef", rank, step))
}

func buildManifest(genDir string, gen, step int64, names []string) (verify.Manifest, error) {
	shards := make([]verify.Shard, 0, len(names))
	for member, name := range names {
		path := filepath.Join(genDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return verify.Manifest{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return verify.Manifest{}, err
		}
		sum := sha256.Sum256(data)
		tail := data
		if len(tail) > verify.TailBytes {
			tail = tail[len(tail)-verify.TailBytes:]
		}
		tailSum := sha256.Sum256(tail)
		shards = append(shards, verify.Shard{
			Name: name, Size: info.Size(), Member: member,
			SHA256: hex.EncodeToString(sum[:]), TailSHA256: hex.EncodeToString(tailSum[:]),
		})
	}
	return verify.Manifest{
		Generation: gen, Step: step, SealedAt: time.Now().UTC().Truncate(time.Second),
		Writer: "ckpt-demo", Shards: shards,
	}, nil
}

func writeManifest(genDir string, m verify.Manifest) error {
	raw, err := m.Marshal()
	if err != nil {
		return err
	}
	tmp := filepath.Join(genDir, ".manifest.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(genDir, "manifest.json"))
}

type resumeDoc struct {
	Schema     int    `json:"schema"`
	Generation int64  `json:"generation"`
	Step       int64  `json:"step"`
	Format     string `json:"format"`
	World      struct {
		Ranks       int  `json:"ranks"`
		TP          int  `json:"tp"`
		PP          int  `json:"pp"`
		DP          int  `json:"dp"`
		Reshardable bool `json:"reshardable"`
	} `json:"world"`
	State struct {
		Model      bool `json:"model"`
		Optimizer  bool `json:"optimizer"`
		Scheduler  bool `json:"scheduler"`
		RNG        bool `json:"rng"`
		Dataloader bool `json:"dataloader"`
		Scaler     bool `json:"scaler"`
		EMA        bool `json:"ema"`
	} `json:"state"`
}

func writeResume(genDir string, gen, step int64) error {
	var doc resumeDoc
	doc.Schema, doc.Generation, doc.Step, doc.Format = 1, gen, step, "dcp"
	doc.World.Ranks, doc.World.TP, doc.World.PP, doc.World.DP = 2, 1, 1, 1
	doc.State.Model, doc.State.Optimizer, doc.State.Scheduler = true, true, true
	doc.State.RNG, doc.State.Dataloader = true, true
	tmp := filepath.Join(genDir, ".resume.json.tmp")
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(genDir, "resume.json"))
}

func applyFault(genDir string, sc Scenario) error {
	path := filepath.Join(genDir, "rank1.pt")
	switch sc {
	case ScenarioClean:
		return nil
	case ScenarioKill:
		return os.Remove(path)
	case ScenarioTorn:
		f, err := os.OpenFile(path, os.O_RDWR, 0o644)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.WriteAt([]byte{0xDE, 0xAD, 0xBE, 0xEF}, info.Size()-4); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	case ScenarioShort:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		return os.Truncate(path, info.Size()-8)
	default:
		return fmt.Errorf("unknown scenario %q", sc)
	}
}

func faultSentence(sc Scenario) string {
	switch sc {
	case ScenarioKill:
		return "rank1.pt deleted — the kill landed between the shards and the manifest"
	case ScenarioTorn:
		return "rank1.pt last 4 bytes overwritten — the tail no longer matches"
	case ScenarioShort:
		return "rank1.pt truncated — the size no longer matches"
	default:
		return "none — this copy is whole"
	}
}

func writeAtomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
