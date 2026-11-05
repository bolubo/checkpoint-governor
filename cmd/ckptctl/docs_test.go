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

// A page that describes an integration has to hand the user something they can paste. All three
// pages have one section of that shape: venv first (PEP 668 is the machine this was measured on),
// then the same entry import, then the object. The pages differ in what they hand the framework.
func TestIntegrationPagesHaveAPasteBlock(t *testing.T) {
	for _, name := range []string{"huggingface.md", "lightning.md", "composer.md"} {
		t.Run(name, func(t *testing.T) {
			page := readDoc(t, name)
			section, ok := sectionOf(page, "## Paste this")
			if !ok {
				t.Fatalf("%s has no '## Paste this' section", name)
			}
			for _, want := range []string{"python3 -m venv .venv", ". .venv/bin/activate", "pip install ckpt-policy"} {
				if !strings.Contains(section, want) {
					t.Fatalf("%s: the paste block is missing %q:\n%s", name, want, section)
				}
			}
			// The entry import is byte-identical on every page: moving between frameworks must not
			// mean relearning where the object comes from.
			code := firstPythonBlock(t, section)
			if got := strings.SplitN(code, "\n", 2)[0]; got != "from ckpt_policy import Governor" {
				t.Fatalf("%s: the python block starts with %q", name, got)
			}
		})
	}
}

// The install page is copied by a person on a machine we do not control. On this one, pip refuses the
// system interpreter (PEP 668), so the venv has to come first in the page a reader follows.
func TestInstallDocsUseAVenv(t *testing.T) {
	section, ok := sectionOf(readDoc(t, "install-python.md"), "## 1. Install")
	if !ok {
		t.Fatal("install-python.md has no Install section")
	}
	venv := strings.Index(section, "python3 -m venv .venv")
	install := strings.Index(section, "pip install ckpt-policy")
	if venv < 0 || install < 0 || venv > install {
		t.Fatalf("the install steps do not start with a venv:\n%s", section)
	}
	if !strings.Contains(section, "PEP 668") {
		t.Fatalf("the page does not say why the venv is needed:\n%s", section)
	}
}

func readDoc(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
	if err != nil {
		t.Fatalf("cannot read docs/%s: %v", name, err)
	}
	return string(raw)
}

// sectionOf returns the text under a heading, up to the next heading of the same or higher level.
func sectionOf(page, heading string) (string, bool) {
	i := strings.Index(page, heading)
	if i < 0 {
		return "", false
	}
	rest := page[i+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest, true
}

func firstPythonBlock(t *testing.T, section string) string {
	t.Helper()
	const fence = "```python\n"
	i := strings.Index(section, fence)
	if i < 0 {
		t.Fatalf("no python block:\n%s", section)
	}
	rest := section[i+len(fence):]
	j := strings.Index(rest, "\n```")
	if j < 0 {
		t.Fatalf("unterminated python block:\n%s", section)
	}
	return rest[:j+1]
}

// F10's "zero dead links": a page that tells a user where to look must not send them nowhere. This
// walks every page under the docs tree and checks that each relative link resolves to a
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

// pageInComment finds a documentation page named by path in a comment. A numbered decision
// ("decision 0015") is a stable identifier and is allowed; a page path is not.
var pageInComment = regexp.MustCompile(`docs/[A-Za-z0-9_./-]*\.md`)

// decisionInComment finds a numbered decision cited in a comment.
var decisionInComment = regexp.MustCompile(`decision (0\d{3})`)

// A comment must not name a documentation page by path.
//
// The top controllers do not do it: node-problem-detector, keda and kube-state-metrics cite no
// internal page from Go code (the matches for "docs/" are external URLs), karpenter cites none, and
// Kubernetes cites design records as KEP URLs — a stable identifier, not a path. A path in a comment
// rots silently: this repository moved three pages and folded fourteen records in one day, and every
// path citation became a lie the compiler cannot see.
//
// What a comment may do is name a numbered decision, which resolves through the decision index; that
// index is checked here too, so a number cannot be cited after the record behind it is deleted. Data
// paths (the world example files) are inputs a test reads, not pages, and the pattern above does not
// match them.
func TestCommentsCiteDecisionsNotPagePaths(t *testing.T) {
	cited := map[string]bool{}
	files := 0

	err := filepath.WalkDir("../..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		for i, line := range strings.Split(string(raw), "\n") {
			idx := strings.Index(line, "//")
			if idx < 0 {
				// "//" inside a string literal (a URL) is not a comment.
				continue
			}
			if strings.Contains(line[:idx], `"`) || strings.Contains(line[:idx], "`") {
				continue
			}
			comment := line[idx:]
			if pageInComment.MatchString(comment) {
				t.Errorf("%s:%d cites a page path instead of a decision: %s", path, i+1, comment)
			}
			for _, m := range decisionInComment.FindAllStringSubmatch(comment, -1) {
				cited[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cannot walk the repository: %v", err)
	}
	if len(cited) == 0 {
		t.Fatal("no numbered decisions found in comments: the pattern changed and this test stopped looking")
	}

	// Every cited number must be described in the decision tree: an index row, or a history entry.
	index, err := os.ReadFile("../../docs/decisions/README.md")
	if err != nil {
		t.Fatalf("cannot read the decision index: %v", err)
	}
	history, err := os.ReadFile("../../docs/decisions/history.md")
	if err != nil {
		t.Fatalf("cannot read the decision history: %v", err)
	}
	described := string(index) + string(history)
	for n := range cited {
		if !strings.Contains(described, n) {
			t.Errorf("decision %s is cited in code but neither indexed nor in history", n)
		}
	}
	t.Logf("code citations: %d decisions, %d Go files, 0 page paths", len(cited), files)
}
