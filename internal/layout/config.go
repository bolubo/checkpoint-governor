package layout

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/resume"
)

// Config is the user's own answer to "my writer has a layout you do not know". It is opt-in: without
// it, only the built-in layouts are looked for, because a glob we invent could match a directory that
// is not a checkpoint at all.
type Config struct {
	Glob        string `json:"glob"`
	StepRegex   string `json:"step_regex"`
	Declaration string `json:"declaration"`
	ProbeCmd    string `json:"probe_cmd"`
}

// ConfigPath is where a user puts the config, inside the directory the governor already owns.
const ConfigPath = ".ckpt-governor/layout.json"

// MaxConfigBytes bounds a file we read on every restore, the same reason the declaration has a bound.
const MaxConfigBytes = 16 << 10

// ProbeTimeout is the whole budget for a user-supplied probe. A probe that hangs must not hang a restore.
// It is a variable so a test can shorten it; production keeps five seconds.
var ProbeTimeout = 5 * time.Second

// LoadConfig reads `<root>/.ckpt-governor/layout.json`. A missing file is not an error: a job that
// uses a built-in layout never writes one.
func LoadConfig(root string) (Config, bool, error) {
	path := filepath.Join(root, ConfigPath)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	if info.Size() > MaxConfigBytes {
		return Config{}, true, fmt.Errorf("%s is %d bytes, more than the %d allowed", ConfigPath, info.Size(), MaxConfigBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, true, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, true, fmt.Errorf("%s: %w", ConfigPath, err)
	}
	return c, true, nil
}

// probeResult is the JSON a `probe_cmd` must print: what the copy holds and what a resume of this job
// needs. Nothing else is read — a probe is an answer, not an extension point.
type probeResult struct {
	State    resume.State `json:"state"`
	Requires []string     `json:"requires"`
}

// probe asks the user's command what this copy holds. It runs only because the user declared it here,
// never because a checkpoint file mentioned it: an untrusted file must not become a command.
func probe(c Copy, cfg Config) (resume.File, error) {
	if cfg.ProbeCmd == "" {
		return resume.File{}, errors.New("no probe_cmd")
	}
	dir := c.Dir
	if c.Target != "" && c.Target != "." {
		dir = filepath.Join(c.Dir, c.Target)
	}
	cmd := strings.ReplaceAll(cfg.ProbeCmd, "{dir}", dir)
	out, err := runProbe(cmd)
	if err != nil {
		return resume.File{}, err
	}
	var p probeResult
	if err := json.Unmarshal(out, &p); err != nil {
		return resume.File{}, fmt.Errorf("probe_cmd printed something that is not JSON: %w", err)
	}
	return resume.File{
		Schema:     resume.Schema,
		Generation: c.Step,
		Step:       c.Step,
		Format:     resume.FormatOther,
		World:      resume.World{Ranks: 1},
		State:      p.State,
		Requires:   p.Requires,
	}, nil
}

// runProbe executes a user-supplied command with a deadline. It is deliberately shell-free: the config
// is a string the user wrote, and splitting it on spaces would silently change what they meant.
func runProbe(command string) ([]byte, error) {
	fields, err := splitCommand(command)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(fields[0], fields[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("probe_cmd failed: %w", err)
	}
	var timedOut atomic.Bool
	timer := time.AfterFunc(ProbeTimeout, func() {
		timedOut.Store(true)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	if err := cmd.Wait(); err != nil {
		if timedOut.Load() {
			return nil, fmt.Errorf("probe_cmd failed: timed out after %s", ProbeTimeout)
		}
		return nil, fmt.Errorf("probe_cmd failed: %w", err)
	}
	return buf.Bytes(), nil
}

// splitCommand splits on whitespace, honouring double quotes. A user who needs more than that writes a
// script and points probe_cmd at it.
func splitCommand(s string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' || r == '\t':
			if inQuote {
				cur.WriteRune(r)
			} else if cur.Len() > 0 {
				fields = append(fields, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, errors.New("probe_cmd has an unclosed quote")
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	if len(fields) == 0 {
		return nil, errors.New("probe_cmd is empty")
	}
	return fields, nil
}

// Declaration returns what a restore should judge this copy against: the declaration beside it, else
// the answer of the user's probe, else nothing. `Probed` says the second answer was used, which the
// caller prints so the user knows which glue made the copy known.
func (c Copy) Declaration(cfg Config) (f resume.File, declared, probed bool, err error) {
	if f, declared, err = c.Load(); declared {
		return f, true, false, err
	}
	if cfg.ProbeCmd == "" {
		return resume.File{}, false, false, nil
	}
	f, err = probe(c, cfg)
	return f, false, true, err
}
