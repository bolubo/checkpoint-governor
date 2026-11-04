package main

import (
	"github.com/bolubo/checkpoint-governor/internal/layout"
	"github.com/bolubo/checkpoint-governor/internal/resume"
)

// ResumeLine is the one sentence a receipt prints about the newest copy: can it continue.
//
// Empty when no copy exists yet — silence is not a verdict, and printing "unknown: nothing sealed yet"
// on every early receipt would be noise. A copy that exists without a declaration *is* printed, as
// `unknown`, because then the silence is the writer's and a person has to hear it (decision 0026).
func ResumeLine(dir string) string {
	cfg, _, err := layout.LoadConfig(dir)
	if err != nil {
		return "resume: unknown: " + err.Error()
	}
	res, err := layout.Scan(dir, cfg)
	if err != nil || (!res.Found && res.Unsealed == nil) {
		return ""
	}
	v := res.Verdict(cfg, resume.World{})
	return "resume: " + v.Line()
}

// ResumeNote is the second sentence a status prints when two layouts claim the same step. It is never
// on a receipt: a receipt line is one line, and the priority is fixed, so the note is information, not
// a second answer.
func ResumeNote(dir string) string {
	cfg, _, err := layout.LoadConfig(dir)
	if err != nil {
		return ""
	}
	res, err := layout.Scan(dir, cfg)
	if err != nil || res.Note == "" {
		return ""
	}
	return "note: " + res.Note
}
