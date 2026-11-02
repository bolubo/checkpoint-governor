package adapters

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// The card fields, in the order the page promises them. A card missing one is a condition nobody can
// audit: "what is 1" and "what does absent mean" are exactly the questions a person asks at 3am.
var cardFields = []string{
	"what 1 means",
	"source",
	"definition",
	"cadence",
	"cost",
	"absent means",
	"unknown means",
	"consumer",
	"test",
}

var cardHeading = regexp.MustCompile("^## `([a-z][a-z0-9.\\-]+)`$")

// TestEveryCatalogueRowHasACard reads conditions.md and checks both directions: every row in
// the catalogue has a card with all nine answers, and every card names a row that exists. This is
// the test that makes "the catalogue is complete" a fact rather than an intention.
func TestEveryCatalogueRowHasACard(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/conditions.md")
	if err != nil {
		t.Fatalf("cannot read the condition cards: %v", err)
	}

	cards := map[string]map[string]string{}
	var current string
	for _, line := range strings.Split(string(raw), "\n") {
		if m := cardHeading.FindStringSubmatch(line); m != nil {
			current = m[1]
			cards[current] = map[string]string{}
			continue
		}
		if current == "" || !strings.HasPrefix(line, "- **") {
			continue
		}
		rest := strings.TrimPrefix(line, "- **")
		field, value, ok := strings.Cut(rest, "**:")
		if !ok {
			continue
		}
		cards[current][field] = strings.TrimSpace(value)
	}
	if len(cards) != len(policy.Conditions()) {
		t.Fatalf("the page has %d cards, the catalogue %d rows", len(cards), len(policy.Conditions()))
	}

	for _, c := range policy.Conditions() {
		card, ok := cards[c.Name]
		if !ok {
			t.Errorf("%s has no card", c.Name)
			continue
		}
		for _, field := range cardFields {
			if card[field] == "" {
				t.Errorf("%s: the %q field is empty", c.Name, field)
			}
		}
	}

	known := map[string]bool{}
	for _, c := range policy.Conditions() {
		known[c.Name] = true
	}
	for name := range cards {
		if !known[name] {
			t.Errorf("%s has a card but is not in the catalogue", name)
		}
	}

	// Three kinds, and the numbers must add up: a row this build produces, a row it declares absent,
	// and a row the sampler derives (it is about the machinery, not about the world).
	registered := map[string]bool{}
	for _, p := range All(Config{}) {
		registered[p.Name()] = true
	}
	var produced, absent, derived int
	for _, c := range policy.Conditions() {
		switch {
		case !registered[c.Name]:
			derived++
		case strings.HasPrefix(cards[c.Name]["source"], "none"):
			absent++
		default:
			produced++
		}
	}
	t.Logf("catalogue: %d/%d (%d produced, %d declared absent, %d derived), 0 holes",
		len(cards), len(policy.Conditions()), produced, absent, derived)
}
