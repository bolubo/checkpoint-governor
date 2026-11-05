package mcp

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/demo"
)

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func call(t *testing.T, s *Server, method string, params any) rpcReply {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, reply := s.Handle(raw)
	if !reply {
		t.Fatalf("%s did not answer", method)
	}
	var got rpcReply
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%s: %v\n%s", method, err, out)
	}
	return got
}

func TestMCPInitializeAndToolList(t *testing.T) {
	s := New([]string{t.TempDir()}, "0.28.0")
	got := call(t, s, "initialize", map[string]any{"protocolVersion": "2024-11-05"})
	if got.Error != nil {
		t.Fatalf("initialize: %+v", got.Error)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(got.Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2024-11-05" || init.ServerInfo.Name != "ckptctl" {
		t.Fatalf("initialize = %+v", init)
	}

	got = call(t, s, "tools/list", map[string]any{})
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(got.Result, &list); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"status": true, "resumable": true, "verify": true, "explain": true, "storage": true, "evidence": true}
	for _, tool := range list.Tools {
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v (got %+v)", want, list.Tools)
	}
}

func TestMCPResumableNamesTheMissingRank(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ckpt")
	if _, err := demo.Create(dir, demo.ScenarioKill); err != nil {
		t.Fatal(err)
	}
	s := New([]string{root}, "0.28.0")
	got := call(t, s, "tools/call", map[string]any{
		"name": "resumable", "arguments": map[string]any{"dir": dir},
	})
	if got.Error != nil {
		t.Fatalf("call: %+v", got.Error)
	}
	var res struct {
		Structured struct {
			State       string `json:"state"`
			Line        string `json:"line"`
			MissingRank int    `json:"missingRank"`
		} `json:"structuredContent"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(got.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Structured.State != "not resumable" || res.Structured.MissingRank != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.Structured.Line == "" {
		t.Fatal("the answer must carry the sentence a person reads")
	}
}

func TestMCPVerifyAll(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ckpt")
	if _, err := demo.Create(dir, demo.ScenarioTorn); err != nil {
		t.Fatal(err)
	}
	s := New([]string{root}, "0.28.0")
	got := call(t, s, "tools/call", map[string]any{
		"name": "verify", "arguments": map[string]any{"dir": dir, "all": true},
	})
	var res struct {
		Structured struct {
			Generations []struct {
				State string `json:"state"`
			} `json:"generations"`
			Tally struct {
				Broken int `json:"broken"`
			} `json:"tally"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(got.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Structured.Generations) != 1 || res.Structured.Tally.Broken != 1 {
		t.Fatalf("verify all = %+v", res.Structured)
	}
}

// The server may read only under --root; an agent must not be able to ask for /etc.
func TestMCPRefusesPathsOutsideRoot(t *testing.T) {
	s := New([]string{t.TempDir()}, "0.28.0")
	got := call(t, s, "tools/call", map[string]any{
		"name": "status", "arguments": map[string]any{"dir": "/etc"},
	})
	var res struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(got.Result, &res); err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a path outside the root must be refused")
	}
}

func TestMCPUnknownToolAndMethod(t *testing.T) {
	s := New([]string{t.TempDir()}, "dev")
	got := call(t, s, "tools/call", map[string]any{"name": "delete", "arguments": map[string]any{}})
	if got.Error == nil || got.Error.Code != -32602 {
		t.Fatalf("unknown tool = %+v", got.Error)
	}
	got = call(t, s, "resources/list", map[string]any{})
	if got.Error == nil || got.Error.Code != -32601 {
		t.Fatalf("unknown method = %+v", got.Error)
	}
}
