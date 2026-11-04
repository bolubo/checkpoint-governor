package main

import "github.com/bolubo/checkpoint-governor/internal/resume"

// ResumeLine is the one sentence a receipt prints about the newest sealed copy: can it continue.
//
// Empty when nothing is sealed yet — silence is not a verdict, and printing "unknown: nothing sealed
// yet" on every early receipt would be noise. "Unknown" is printed when there *is* a copy and nobody
// said what it holds, because then the silence is the writer's and a person has to hear it.
func ResumeLine(dir string) string {
	sealed, ok, err := resume.NewestSealed(dir)
	if err != nil || !ok {
		return ""
	}
	f, declared, loadErr := resume.Load(sealed.Dir)
	v := resume.Judge(f, declared, loadErr, resume.World{})
	return "resume: " + v.Line()
}
