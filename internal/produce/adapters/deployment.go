package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// deploymentFile is the operator's declaration about the deployment itself: where the world lives,
// and which upstreams exist at all.
//
// It is separate from conditions.json on purpose. conditions.json holds thresholds — numbers
// somebody tuned; this holds facts — whether there is a scheduler in this cluster at all. They are
// decided by different people at different times, and a threshold rollout must not be able to flip a
// capability.
const deploymentFile = "deployment.json"

// Capability is one declaration: does this deployment have X upstream, and why.
//
// Undeclared means **assumed on**, and that is a deliberate asymmetry. Writing when nothing was at
// risk costs one checkpoint; missing a preemption costs the job. A deployment that has priorities at
// all almost always has something that enforces them, so the silent default is the dangerous one.
//
// The safety valve is elsewhere: "assumed on" only says to *evaluate* when there is something to
// read. With no observer the condition is absent, not unknown — otherwise a single-job laptop would
// be told, every round, that it cannot see a platform it does not have.
type Capability struct {
	Exists   bool
	Why      string
	Declared bool
}

// Capability names, as they appear in the file and in the catalogue of conditions they feed.
const (
	CapPreemption = "preemption"
	CapEviction   = "eviction"
)

// Deployment is what the operator declared, plus where the world snapshots live.
type Deployment struct {
	WorldDir     string
	Capabilities map[string]Capability
	// GraceSeconds is the shutdown window the platform gives this job, when the operator declared it.
	// `wrap` reads it because the platform already told the job; asking the operator to repeat it on
	// every command line is a second source of truth. HasGrace separates "declared 0" from "absent".
	GraceSeconds int
	HasGrace     bool
}

type deploymentFileShape struct {
	Schema int `json:"schema"`
	World  struct {
		Dir string `json:"dir"`
	} `json:"world"`
	GraceSeconds *int `json:"grace_seconds"`
	Capabilities map[string]struct {
		Exists *bool  `json:"exists"`
		Why    string `json:"why"`
	} `json:"capabilities"`
}

// LoadDeployment reads the declaration. A missing file is not an error: a deployment that declared
// nothing has no upstreams, and every world-derived condition stays absent.
//
// A malformed file is an error and must fail startup: an operator who wrote "exists": tru believes
// preemption is on, and silently ignoring it would leave them believing it.
func LoadDeployment(dir string) (Deployment, error) {
	var d Deployment
	raw, err := os.ReadFile(filepath.Join(dir, ".ckpt-governor", deploymentFile))
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		// The same rule as conditions.json: a directory that cannot be read is the failure
		// `store.blocked` reports. Stopping here would leave the job ungoverned because the thing
		// that reports the damage could not be read — so we assume nothing was declared, which is
		// the same as a deployment that never wrote the file.
		if errors.Is(err, syscall.ENOTDIR) || os.IsPermission(err) {
			return d, nil
		}
		return d, err
	}

	var f deploymentFileShape
	if err := json.Unmarshal(raw, &f); err != nil {
		return d, fmt.Errorf("%s: %w", deploymentFile, err)
	}
	if f.Schema != 0 && f.Schema != 1 {
		return d, fmt.Errorf("%s: unsupported schema %d", deploymentFile, f.Schema)
	}

	d.WorldDir = f.World.Dir
	if f.GraceSeconds != nil {
		if *f.GraceSeconds < 0 {
			return d, fmt.Errorf("%s: grace_seconds %d is negative", deploymentFile, *f.GraceSeconds)
		}
		d.GraceSeconds, d.HasGrace = *f.GraceSeconds, true
	}
	d.Capabilities = make(map[string]Capability, len(f.Capabilities))
	for name, c := range f.Capabilities {
		switch name {
		case CapPreemption, CapEviction:
		default:
			// A typo here would silently do nothing, which is how a deployment ends up believing it
			// turned something on. Name the mistake at startup instead.
			return d, fmt.Errorf("%s: unknown capability %q (known: %s, %s)",
				deploymentFile, name, CapPreemption, CapEviction)
		}
		if c.Exists == nil {
			return d, fmt.Errorf("%s: capability %q has no \"exists\"", deploymentFile, name)
		}
		d.Capabilities[name] = Capability{Exists: *c.Exists, Why: c.Why, Declared: true}
	}
	return d, nil
}

// Capability resolves one declaration, applying the assumed-on default.
func (d Deployment) Capability(name string) Capability {
	if c, ok := d.Capabilities[name]; ok {
		return c
	}
	return Capability{Exists: true, Declared: false}
}

// CapabilityNote is the sentence `doctor` prints for a condition whose answer is decided by a
// declaration rather than by a measurement. ok=false means "nothing to add".
func CapabilityNote(dir, name string) (string, bool) {
	d, err := LoadDeployment(dir)
	if err != nil {
		return "", false
	}
	c, ok := d.Capabilities[name]
	if !ok {
		return "not declared; assumed on (write deployment.json to turn it off)", true
	}
	if !c.Exists {
		if c.Why != "" {
			return "declared off by the operator: " + c.Why, true
		}
		return "declared off by the operator", true
	}
	return "declared on by the operator", true
}

// WorldDir is where the observer writes. The declaration wins over the default; the default is a
// path, not a promise that anything writes there.
func (d Deployment) WorldDirOrDefault(def string) string {
	if d.WorldDir != "" {
		return d.WorldDir
	}
	return def
}

// DeclaredNames lists the capabilities somebody wrote down, for `ckptctl config`.
func (d Deployment) DeclaredNames() []string {
	out := make([]string, 0, len(d.Capabilities))
	for name := range d.Capabilities {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
