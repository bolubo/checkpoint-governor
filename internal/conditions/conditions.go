// Package conditions loads the conditions a deployment adds to the frozen catalogue.
//
// The built-in catalogue is frozen (decision 0013): adding one is a code change, a producer and a
// test. Some conditions, though, belong to the deployment rather than to this repository — "the loss
// exploded", "the scratch volume the team shares is gone" — and the governor cannot know their
// names. This is the one extension point: one JSON file per condition, read by one file probe, and
// appended after the built-ins, so a built-in always wins a tie (decision 0024).
//
// A file probe can only read a file. That is the point: the extension point adds names, not
// authority. A condition that needed a new measurement would be a new probe in the code, with a card
// and a test, which is exactly what a fast path must not bypass.
package conditions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce"
)

// DirName is the directory, inside `.ckpt-governor`, that holds one file per added condition. It sits
// with conditions.json and deployment.json because it is operator configuration, and it travels with
// the job for the same reason: a condition that lives only on the machine that started the controller
// is a condition nobody can audit later.
const DirName = "conditions.d"

// Every is how often a file probe is read. It matches the storage conditions' default: a file the
// job writes is cheap to stat and read, and faster polling would only make the log noisier.
const Every = 5 * time.Second

// conditionName is the shape the catalogue already uses: lower case, dotted, no spaces. A name that
// looks like a built-in must not be able to shadow one, so this is the same grammar as 0013.
var conditionName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// spec is one file in conditions.d.
type spec struct {
	Name    string `json:"name"`
	Tier    string `json:"tier"`
	Missing string `json:"missing"`
	Why     string `json:"why"`
	Read    struct {
		File string `json:"file"`
		On   string `json:"on"`
		Off  string `json:"off"`
	} `json:"read"`
}

// Set is what a directory adds: rows for the policy, probes for the sampler. The two are one object
// because they are one file: a row without a probe is a condition nobody produces.
type Set struct {
	Rows   []policy.Condition
	Probes []produce.Probe
}

// Len is how many conditions this directory adds.
func (s Set) Len() int { return len(s.Rows) }

// Load reads `<dir>/.ckpt-governor/conditions.d/*.json`. A missing directory is not an error: the
// built-in catalogue is the default deployment. A malformed file is an error, and it must fail
// startup — a condition an operator believes is watched, and is not, is worse than a refusal.
func Load(dir string) (Set, error) {
	base := filepath.Join(dir, advise.BaseDir, DirName)
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return Set{}, nil
		}
		return Set{}, err
	}

	builtin := map[string]bool{}
	for _, c := range policy.Conditions() {
		builtin[c.Name] = true
	}
	seen := map[string]string{}

	var set Set
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(base, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return Set{}, err
		}
		var s spec
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		// An unknown key is an operator's typo ("teir"), and silently ignoring it would apply a
		// condition with a default nobody chose.
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return Set{}, fmt.Errorf("%s: %w", name, err)
		}
		if err := s.validate(); err != nil {
			return Set{}, fmt.Errorf("%s: %w", name, err)
		}
		if builtin[s.Name] {
			return Set{}, fmt.Errorf("%s: %s is a built-in condition; a file cannot shadow it", name, s.Name)
		}
		if other, dup := seen[s.Name]; dup {
			return Set{}, fmt.Errorf("%s: %s is already defined by %s", name, s.Name, other)
		}
		seen[s.Name] = name

		set.Rows = append(set.Rows, s.condition())
		set.Probes = append(set.Probes, probe{spec: s})
	}
	return set, nil
}

// validate rejects everything that would make a reading ambiguous. Each check has a same-shaped
// reason: what a person could believe versus what the file actually says.
func (s spec) validate() error {
	if !conditionName.MatchString(s.Name) {
		return fmt.Errorf("name %q must be lower case and dotted, like job.loss-exploded", s.Name)
	}
	if _, ok := parseTier(s.Tier); !ok {
		return fmt.Errorf("tier %q must be urgent, warn or note", s.Tier)
	}
	if _, ok := parseMissing(s.Missing); !ok {
		return fmt.Errorf("missing %q must be inert or urgent", s.Missing)
	}
	if strings.TrimSpace(s.Why) == "" {
		return fmt.Errorf("why is empty: a decision that names this condition must be able to explain it")
	}
	if s.Read.File == "" {
		return fmt.Errorf("read.file is empty: a file probe needs a file")
	}
	if filepath.IsAbs(s.Read.File) || strings.HasPrefix(filepath.Clean(s.Read.File), "..") {
		return fmt.Errorf("read.file %q must be a path inside the checkpoint directory", s.Read.File)
	}
	if s.Read.On == "" || s.Read.Off == "" {
		return fmt.Errorf("read.on and read.off are required: a value can only mean one of two things")
	}
	if s.Read.On == s.Read.Off {
		return fmt.Errorf("read.on and read.off are the same value %q, so the condition can never be 0", s.Read.On)
	}
	return nil
}

func (s spec) condition() policy.Condition {
	tier, _ := parseTier(s.Tier)
	missing, _ := parseMissing(s.Missing)
	return policy.Condition{Name: s.Name, Tier: tier, Missing: missing, Why: s.Why}
}

func parseTier(v string) (policy.Tier, bool) {
	switch v {
	case "urgent":
		return policy.TierUrgent, true
	case "warn":
		return policy.TierWarn, true
	case "note":
		return policy.TierNote, true
	}
	return policy.TierNote, false
}

func parseMissing(v string) (policy.MissingPolicy, bool) {
	switch v {
	case "inert":
		return policy.MissingInert, true
	case "urgent":
		return policy.MissingUrgent, true
	}
	return policy.MissingInert, false
}

// probe is the one producer a disk condition gets: it reads a file the job writes and compares it
// with the two values the operator declared.
//
// A missing file reports unknown, never 0: whether "the file is not there" means calm or blind is the
// operator's declaration (`missing`), not a choice this probe makes. A value that is neither on nor
// off is also unknown, in the fail-closed direction: a job that half-wrote the file must not look
// calm.
type probe struct {
	spec spec
}

func (p probe) Name() string { return p.spec.Name }

func (p probe) Every() time.Duration { return Every }

func (p probe) Cost() produce.Cost { return produce.Cheap }

func (p probe) Read(env produce.Env) produce.Reading {
	path := filepath.Join(env.Dir, p.spec.Read.File)
	raw, err := os.ReadFile(path)
	if err != nil {
		return produce.Reading{State: produce.Unknown, At: env.Now, Err: err}
	}
	switch v := strings.TrimSpace(string(raw)); v {
	case p.spec.Read.On:
		return produce.Reading{State: produce.Fresh, Value: true, At: env.Now}
	case p.spec.Read.Off:
		return produce.Reading{State: produce.Fresh, Value: false, At: env.Now}
	default:
		return produce.Reading{State: produce.Unknown, At: env.Now,
			Err: fmt.Errorf("%s holds %q, which is neither %q nor %q",
				p.spec.Read.File, v, p.spec.Read.On, p.spec.Read.Off)}
	}
}
