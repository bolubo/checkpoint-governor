package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// generationDir matches the directory names a writer uses, so a walk can order generations as numbers
// instead of as strings (where gen-10 sorts before gen-9).
var generationDir = regexp.MustCompile(`^gen-(\d+)$`)

// Summary is every generation in a root, oldest first, plus the tally.
type Summary struct {
	Receipts []Receipt
	Verdicts []Verdict
	Tally    Tally
}

// Summarize walks a checkpoint root and reports every generation it finds.
//
// This is the "was it worth running" view: one line per generation and the two numbers that decide it. The
// tally's `Taken` cannot be known from a directory — it is the scheduler's fact — so it stays zero here and
// `VerifiedPerTaken` is only meaningful once a caller supplies it.
func Summarize(root string, want int64, opts Options) (Summary, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return Summary{}, err
	}

	type gen struct {
		n   int64
		dir string
	}
	var gens []gen
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := generationDir.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		gens = append(gens, gen{n: n, dir: filepath.Join(root, e.Name())})
	}
	sort.Slice(gens, func(i, j int) bool { return gens[i].n < gens[j].n })

	out := Summary{}
	for _, g := range gens {
		v := Classify(g.dir, want, opts)
		out.Verdicts = append(out.Verdicts, v)

		r := Receipt{
			Generation: g.n,
			Step:       v.Step,
			SealedAt:   v.SealedAt,
			Reason:     v.Reason,
			Detail:     v.Detail,
		}
		switch v.State {
		case policy.CopyVerified:
			r.Check = "sample"
			if opts.Full {
				r.Check = "full"
			}
			r.VerifiedAt = opts.Now
			out.Tally.Verified++
		case policy.CopyBroken:
			// From a walk, a broken generation counts as invalid: it exists, and it is not a copy anybody
			// can come back to. Whether a rewrite replaced it is the loop's knowledge, not the disk's.
			out.Tally.Invalid++
		default:
			// Sealed but unread, or a write in flight: a generation that exists and is not yet a verdict.
			out.Tally.Unchecked++
		}
		out.Receipts = append(out.Receipts, r)
	}
	return out, nil
}

// NowOr returns the time a summary should be read against.
func NowOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
