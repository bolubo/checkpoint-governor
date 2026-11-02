package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A manifest that goes in comes out identical: the fixture is the contract, and a parser that silently
// reshapes it would make "the file says X" untrustworthy.
func TestAManifestRoundTripsByteForByte(t *testing.T) {
	for _, dir := range []string{"sealed-ok", "stale-generation"} {
		path := filepath.Join("testdata", dir, versionDir(t, dir), "manifest.json")
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		m, err := Parse(original)
		if err != nil {
			t.Fatalf("%s: parse: %v", dir, err)
		}
		again, err := m.Marshal()
		if err != nil {
			t.Fatalf("%s: marshal: %v", dir, err)
		}
		if string(again) != string(original) {
			t.Fatalf("%s: round trip changed the bytes\n--- have ---\n%s\n--- want ---\n%s", dir, again, original)
		}
	}
}

// Every refusal names what was wrong. A manifest is untrusted input: it arrives on the customer's volume,
// written by their library, so "it did not parse" is not good enough to debug with.
func TestBadManifestsAreRefusedByName(t *testing.T) {
	cases := []struct {
		fixture string
		want    string
	}{
		{"evil-path", "climbs out"},
		{"unknown-field", "does not parse"},
	}

	for _, c := range cases {
		path := filepath.Join("testdata", c.fixture, versionDir(t, c.fixture), "manifest.json")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", c.fixture, err)
		}
		_, err = Parse(b)
		if err == nil {
			t.Fatalf("%s: parsed, want a refusal", c.fixture)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: error %q does not mention %q", c.fixture, err, c.want)
		}
	}
}

// The rules that exist because the input is a claim rather than a fact.
func TestManifestRules(t *testing.T) {
	ok := Manifest{
		Generation: 7, Step: 1, SealedAt: fixedTime(), Writer: "w",
		Shards: []Shard{{Name: "gen-0007/rank-00.bin", Size: 10, SHA256: strings.Repeat("a", 64)}},
	}
	if err := ok.validate(); err != nil {
		t.Fatalf("a valid manifest was refused: %v", err)
	}

	cases := []struct {
		name string
		edit func(*Manifest)
		want string
	}{
		{"no generation", func(m *Manifest) { m.Generation = 0 }, "not positive"},
		{"no shards", func(m *Manifest) { m.Shards = nil }, "names no shards"},
		{"unsealed", func(m *Manifest) { m.SealedAt = time.Time{} }, "sealedAt"},
		{"absolute path", func(m *Manifest) { m.Shards[0].Name = "/etc/passwd" }, "absolute"},
		{"traversal", func(m *Manifest) { m.Shards[0].Name = "../x" }, "climbs out"},
		{"not canonical", func(m *Manifest) { m.Shards[0].Name = "gen-0007/./x" }, "canonical"},
		{"negative size", func(m *Manifest) { m.Shards[0].Size = -1 }, "outside what any volume holds"},
		{"absurd size", func(m *Manifest) { m.Shards[0].Size = MaxShardBytes + 1 }, "outside what any volume holds"},
		{"bad hash", func(m *Manifest) { m.Shards[0].SHA256 = "xyz" }, "malformed sha256"},
		{"too many shards", func(m *Manifest) {
			m.Shards = make([]Shard, MaxShards+1)
			for i := range m.Shards {
				m.Shards[i] = ok.Shards[0]
			}
		}, "more than"},
	}

	for _, c := range cases {
		m := ok
		m.Shards = append([]Shard(nil), ok.Shards...)
		c.edit(&m)
		err := m.validate()
		if err == nil {
			t.Fatalf("%s: accepted", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}
}
