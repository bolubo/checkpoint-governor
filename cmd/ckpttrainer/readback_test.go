package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// A shard is described by the same two facts the manifest carries: its size and its tail hash.
func describeShard(t *testing.T, dir, name string) verify.Shard {
	t.Helper()
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	tail := data
	if len(tail) > verify.TailBytes {
		tail = tail[len(tail)-verify.TailBytes:]
	}
	tailSum := sha256.Sum256(tail)
	return verify.Shard{
		Name: name, Size: info.Size(),
		SHA256: hex.EncodeToString(sum[:]), TailSHA256: hex.EncodeToString(tailSum[:]),
	}
}

func TestReadbackAcceptsWhatItWrote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rank-00.bin")
	if err := os.WriteFile(path, []byte("weights-and-optimizer"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := describeShard(t, dir, "rank-00.bin")
	if err := readbackShards(dir, []verify.Shard{s}); err != nil {
		t.Fatalf("a shard that is there must read back: %v", err)
	}
}

func TestReadbackNamesAMissingShard(t *testing.T) {
	dir := t.TempDir()
	s := verify.Shard{Name: "rank-01.bin", Size: 8, SHA256: strings.Repeat("a", 64)}
	err := readbackShards(dir, []verify.Shard{s})
	if err == nil {
		t.Fatal("a missing shard must not pass the readback")
	}
	if !strings.Contains(err.Error(), "rank-01.bin") {
		t.Fatalf("the error must name the shard: %v", err)
	}
}

func TestReadbackRefusesAShortShard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rank-00.bin")
	if err := os.WriteFile(path, []byte("short"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := describeShard(t, dir, "rank-00.bin")
	s.Size += 100
	err := readbackShards(dir, []verify.Shard{s})
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("a shard smaller than sealed must fail with its sizes: %v", err)
	}
}

// A kill lands at the end of a shard, not in the middle: the tail check is the one that catches it.
func TestReadbackRefusesATornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rank-00.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := describeShard(t, dir, "rank-00.bin")
	// Same size, different last byte: exactly what a tear looks like.
	if err := os.WriteFile(path, []byte("012345678X"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := readbackShards(dir, []verify.Shard{s})
	if err == nil || !strings.Contains(err.Error(), "tail hashes") {
		t.Fatalf("a torn tail must fail: %v", err)
	}
}

// The contract writer must not seal what it could not read back, while `--tear` must still produce the
// damaged copy the fault matrix judges: the tear is injected after the readback, not before it.
func TestTheContractWriterSealsOnlyAfterTheReadback(t *testing.T) {
	c := config{dir: t.TempDir(), ranks: 1, shardBytes: 4096, contract: true}
	base := filepath.Join(c.dir, ".ckpt-governor")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeContractCheckpoint(c, base, 7, &trace{}); err != nil {
		t.Fatal(err)
	}
	gen := filepath.Join(c.dir, "gen-0007")
	v := verify.Classify(gen, 7, verify.Options{CheckContent: true})
	if v.State != policy.CopyVerified {
		t.Fatalf("a clean save must verify: %s %s", v.State, v.Detail)
	}

	c.tear = true
	if err := writeContractCheckpoint(c, base, 8, &trace{}); err != nil {
		t.Fatalf("--tear is a test fixture and must still seal a damaged copy: %v", err)
	}
	gen = filepath.Join(c.dir, "gen-0008")
	v = verify.Classify(gen, 8, verify.Options{CheckContent: true})
	if v.State != policy.CopyBroken {
		t.Fatalf("the torn copy must read as broken, got %s", v.State)
	}
}
