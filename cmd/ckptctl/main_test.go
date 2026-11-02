package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// The help screen is the surface: a verb that is not on it cannot be found, and a verb listed twice
// cannot be trusted. Both are cheap to check here and expensive to notice by eye.
func TestUsageNamesEveryCommandOnceInThreeGroups(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	out := buf.String()

	if want := "usage: ckptctl <command>"; !strings.Contains(out, want) {
		t.Errorf("usage has no %q line", want)
	}

	groups := []string{groupLoop, groupRead, groupOther}
	if n := strings.Count(out, "\n\n"); n != len(groups) {
		t.Errorf("usage printed %d group headings, want %d", n, len(groups))
	}

	seen := map[string]bool{}
	for _, c := range commands {
		if seen[c.name] {
			t.Errorf("%s appears in the table twice", c.name)
		}
		seen[c.name] = true
		if c.group != groupLoop && c.group != groupRead && c.group != groupOther {
			t.Errorf("%s has group %q, which the help does not print", c.name, c.group)
		}
		line := regexp.MustCompile("(?m)^  " + regexp.QuoteMeta(c.name) + ` +` + regexp.QuoteMeta(c.one) + "$")
		if !line.MatchString(out) {
			t.Errorf("%s is not on the help screen with its one line", c.name)
		}
	}

	// The first group is the three verbs a new user needs. The README and the plan both promise three,
	// so the group's contents are a claim rather than a preference.
	var loop []string
	for _, c := range commands {
		if c.group == groupLoop {
			loop = append(loop, c.name)
		}
	}
	if got, want := strings.Join(loop, ","), "init,price,run"; got != want {
		t.Errorf("the first group is %s, want %s", got, want)
	}
}
