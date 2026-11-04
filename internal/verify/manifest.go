// Package verify reads what a checkpoint writer left behind, and says what it is.
//
// It never writes a checkpoint, never repairs one, and never deletes one. The data path belongs to the
// workload; this package's whole job is to answer the question a receipt needs: **is the thing on disk
// the thing that was asked for, and is it whole** (0025).
package verify

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

// MaxShards bounds how many entries a manifest may declare. A manifest is untrusted input: it arrives on
// the customer's volume, written by their library, read by us.
const MaxShards = 4096

// MaxShardBytes bounds what one shard may claim. A manifest that declares a petabyte on a hundred-megabyte
// volume is not a checkpoint, it is a bug or an attack, and either way it must not become an allocation.
const MaxShardBytes = 1 << 40

// Manifest is what a writer seals after every shard has been written.
//
// The ordering is the point: a manifest that exists means every shard it names was already written, which
// is the only reason a rename of one small file is worth a protocol. A generation directory with no
// manifest is a write in flight, not a broken copy — and that difference decides whether we wait or ask
// again.
type Manifest struct {
	Generation int64     `json:"generation"`
	Step       int64     `json:"step"`
	SealedAt   time.Time `json:"sealedAt"`
	Writer     string    `json:"writer"`
	Shards     []Shard   `json:"shards"`
}

// Shard is one file of the checkpoint, opaque to us: the size and the hash are the whole of our
// understanding, deliberately. Format awareness is v2's problem (0025).
type Shard struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Member is which rank wrote this shard, when the writer knows. Zero is the single-writer answer
	// and every manifest written before this field had one; a multi-rank copy declares `world.ranks`
	// and can then be checked for every member's shard (0026).
	Member int `json:"member,omitempty"`
	// TailSHA256 is the hash of the **last** min(size, TailBytes) bytes.
	//
	// It exists because the cheap check needs something to compare against, and a kill leaves its damage
	// at the end of a shard. Without this field content can only be checked by reading every byte — which
	// is affordable once, not on every save.
	TailSHA256 string `json:"tailSha256,omitempty"`
}

// Marshal produces the exact bytes a fixture holds, so a manifest that goes in comes out identical.
func (m Manifest) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Parse reads a manifest and refuses anything it cannot believe.
//
// Every rule here exists because the input is untrusted: a shard name is a path, a size is a claim, and
// the generation is what keeps a late writer from passing a stale copy off as the answer to a newer
// request.
func Parse(b []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest does not parse: %w", err)
	}
	if err := m.validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m Manifest) validate() error {
	if m.Generation <= 0 {
		return fmt.Errorf("manifest generation %d is not positive", m.Generation)
	}
	if m.Step < 0 {
		return fmt.Errorf("manifest step %d is negative", m.Step)
	}
	if m.SealedAt.IsZero() {
		return fmt.Errorf("manifest has no sealedAt: an unsealed manifest must not exist")
	}
	if len(m.Shards) == 0 {
		return fmt.Errorf("manifest generation %d names no shards", m.Generation)
	}
	if len(m.Shards) > MaxShards {
		return fmt.Errorf("manifest declares %d shards, more than the %d allowed", len(m.Shards), MaxShards)
	}
	for _, s := range m.Shards {
		if err := s.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s Shard) validate() error {
	if s.Name == "" {
		return fmt.Errorf("a shard has no name")
	}
	if path.IsAbs(s.Name) {
		return fmt.Errorf("shard name %q is absolute", s.Name)
	}
	if strings.Contains(s.Name, "..") {
		return fmt.Errorf("shard name %q climbs out of the generation directory", s.Name)
	}
	if clean := path.Clean(s.Name); clean != s.Name {
		return fmt.Errorf("shard name %q is not in canonical form (%q)", s.Name, clean)
	}
	if s.Size < 0 || s.Size > MaxShardBytes {
		return fmt.Errorf("shard %q declares %d bytes, outside what any volume holds", s.Name, s.Size)
	}
	if len(s.SHA256) != 64 || strings.Trim(s.SHA256, "0123456789abcdef") != "" {
		return fmt.Errorf("shard %q has a malformed sha256 %q", s.Name, s.SHA256)
	}
	if s.TailSHA256 != "" && (len(s.TailSHA256) != 64 || strings.Trim(s.TailSHA256, "0123456789abcdef") != "") {
		return fmt.Errorf("shard %q has a malformed tail sha256 %q", s.Name, s.TailSHA256)
	}
	return nil
}
