package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// markdownLink finds [text](target). It is deliberately narrow: it must not fire on code blocks that
// show a shell command, and it does not try to parse Markdown in general.
var markdownLink = regexp.MustCompile(`\[[^\]]+\]\(([^)\s]+)\)`)

// F10's "zero dead links": a page that tells a user where to look must not send them nowhere. This
// walks every page in docs/ and docs/decisions/ and checks that each relative link resolves to a
// file that exists. The decisions directory is included because a folded record is exactly where a
// stale pointer hides: the page is gone, and the citation in another page still names it.
func TestDocsHaveNoDeadLinks(t *testing.T) {
	docs, err := filepath.Glob("../../docs/*.md")
	if err != nil || len(docs) == 0 {
		t.Fatalf("no docs found: %v", err)
	}
	records, err := filepath.Glob("../../docs/decisions/*.md")
	if err != nil {
		t.Fatalf("cannot list decisions: %v", err)
	}
	docs = append(docs, records...)

	links := 0
	for _, page := range docs {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("cannot read %s: %v", page, err)
		}
		for _, m := range markdownLink.FindAllStringSubmatch(string(raw), -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			// Strip a fragment: the file is what we can check, and an anchor is checked by the eye.
			path := strings.SplitN(target, "#", 2)[0]
			if path == "" {
				continue
			}
			links++
			resolved := filepath.Join(filepath.Dir(page), path)
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s links to %s, which does not exist", filepath.Base(page), target)
			}
		}
	}
	t.Logf("docs: %d pages, %d local links, 0 dead", len(docs), links)
}

// conditionHeading finds a card title: "## `risk.preempt`".
var conditionHeading = regexp.MustCompile("(?m)^## `([a-z0-9.-]+)`")

// The catalogue and the cards describe the same set, in both directions.
//
// A condition that exists without a card is one nobody can look up the meaning of; a card that
// survives a deleted condition is a page telling people about something the product no longer
// decides. A reviewer glancing at a diff catches one of those, and not always the same one, so both
// directions are checked here.
func TestEveryConditionHasACard(t *testing.T) {
	raw, err := os.ReadFile("../../docs/conditions.md")
	if err != nil {
		t.Fatalf("cannot read docs/conditions.md: %v", err)
	}

	cards := map[string]bool{}
	for _, m := range conditionHeading.FindAllStringSubmatch(string(raw), -1) {
		cards[m[1]] = true
	}
	if len(cards) == 0 {
		t.Fatal("no cards found: the heading format changed and this test stopped looking")
	}

	for _, c := range policy.Conditions() {
		if !cards[c.Name] {
			t.Errorf("condition %s has no card in docs/conditions.md", c.Name)
		}
		delete(cards, c.Name)
	}
	for name := range cards {
		t.Errorf("docs/conditions.md documents %s, which is not in the catalogue", name)
	}
}
