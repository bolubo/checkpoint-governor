package layout

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/bolubo/checkpoint-governor/internal/resume"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// Verdict is the one place a discovered copy becomes an answer. Every caller — `resumable`, `status`,
// the receipt, the loop — goes through it, so the same copy cannot be described two ways (decision 0026).
func (r Result) Verdict(cfg Config, asked resume.World) resume.Verdict {
	if !r.Found {
		if r.Unsealed != nil {
			return resume.Verdict{Reason: resume.ReasonUnstated,
				Detail: fmt.Sprintf("%s is not sealed", r.Unsealed.Artifact())}
		}
		return resume.Verdict{}
	}
	return Judge(r.Newest, cfg, asked)
}

// Judge turns one copy into a verdict, using the three glues in order: the declaration beside it, the
// user's probe, else an actionable "unknown". Silence is never reported as "not resumable": the copy
// may be perfect, and nobody said what it holds.
func Judge(c Copy, cfg Config, asked resume.World) resume.Verdict {
	f, declared, probed, err := c.Declaration(cfg)
	if !declared && !probed {
		return resume.Verdict{Reason: resume.ReasonUnstated,
			Detail: fmt.Sprintf("no declaration beside %s (and no probe_cmd to ask)", c.Artifact())}
	}
	if probed && err != nil {
		return resume.Verdict{Reason: resume.ReasonUnstated, Detail: err.Error()}
	}
	if declared && err != nil {
		return resume.Verdict{Reason: resume.ReasonInvalid, Detail: err.Error(), Declared: true}
	}
	v := resume.Judge(f, true, nil, asked)
	return checkShards(c, f, v)
}

// checkShards refuses a copy that a multi-rank job wrote and that does not hold every rank's shard.
//
// It only fires when the manifest actually marks ranks (a shard with `member` above zero): a writer
// that never recorded membership left us nothing to check, and inventing a missing rank would be a
// guess. A copy whose shard is missing from the volume is not resumable, whatever its declaration
// claims (0026).
func checkShards(c Copy, decl resume.File, v resume.Verdict) resume.Verdict {
	if !v.Resumable() || c.Target != "." || decl.World.Ranks <= 1 {
		return v
	}
	raw, err := os.ReadFile(filepath.Join(c.Dir, "manifest.json"))
	if err != nil {
		return v
	}
	m, err := verify.Parse(raw)
	if err != nil {
		return v
	}
	seen := map[int]bool{}
	marked := false
	for _, sh := range m.Shards {
		seen[sh.Member] = true
		if sh.Member > 0 {
			marked = true
		}
	}
	if !marked {
		return v
	}
	for rank := 0; rank < decl.World.Ranks; rank++ {
		if !seen[rank] {
			v.Reason = resume.ReasonShardMissing
			v.MissingRank = rank
			v.Missing = nil
			return v
		}
	}
	return v
}
