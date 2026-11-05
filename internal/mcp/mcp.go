// Package mcp is the read-only MCP view of the same facts the CLI prints. It exists so an agent can
// answer "why did this run not resume" without a person translating a table, and it deliberately has
// no write tools: nothing here can change a checkpoint, publish a plan, or touch a cluster.
//
// Every tool is a thin adapter over `evidence`, `verify` and `advise`; the bundle schema and the
// verdicts are the ones the commands already own.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/evidence"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/resume"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// Server answers the MCP protocol on one stream. Roots bound every path a tool may read.
type Server struct {
	Roots   []string
	Version string
}

// New builds a server whose tools may read only under roots.
func New(roots []string, version string) *Server {
	return &Server{Roots: roots, Version: version}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content           []content `json:"content"`
	StructuredContent any       `json:"structuredContent,omitempty"`
	IsError           bool      `json:"isError,omitempty"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Serve reads one JSON-RPC message per line until the stream ends. Notifications (messages with no
// id) are executed when they have an effect and never answered, as the protocol requires.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		resp, reply := s.Handle(line)
		if !reply {
			continue
		}
		if _, err := out.Write(append(resp, '\n')); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Handle answers one message. The bool says whether a response is due.
func (s *Server) Handle(raw []byte) ([]byte, bool) {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return errorBytes(nil, -32700, "parse error: "+err.Error()), true
	}
	reply := len(req.ID) > 0 && string(req.ID) != "null"

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := p.ProtocolVersion
		if version == "" {
			version = "2024-11-05"
		}
		return okBytes(req.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "ckptctl", "version": s.Version},
		}), reply
	case "notifications/initialized":
		return nil, false
	case "tools/list":
		return okBytes(req.ID, map[string]any{"tools": tools()}), reply
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errorBytes(req.ID, -32602, "invalid params: "+err.Error()), reply
		}
		result := s.call(p.Name, p.Arguments)
		if result == nil {
			return errorBytes(req.ID, -32602, "unknown tool "+p.Name), reply
		}
		return okBytes(req.ID, result), reply
	default:
		return errorBytes(req.ID, -32601, "method not found: "+req.Method), reply
	}
}

func (s *Server) call(name string, args json.RawMessage) *callResult {
	switch name {
	case "status", "resumable", "evidence", "storage":
		var a struct {
			Dir string `json:"dir"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return failed("invalid arguments: " + err.Error())
		}
		dir, err := s.resolve(a.Dir)
		if err != nil {
			return failed(err.Error())
		}
		b := evidence.Collect(dir)
		switch name {
		case "status":
			return okResult(b.Status)
		case "resumable":
			return okResult(b.Resume)
		case "storage":
			return okResult(b.Storage)
		default:
			return okResult(b)
		}
	case "verify":
		var a struct {
			Dir        string `json:"dir"`
			Generation int64  `json:"generation"`
			All        bool   `json:"all"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return failed("invalid arguments: " + err.Error())
		}
		dir, err := s.resolve(a.Dir)
		if err != nil {
			return failed(err.Error())
		}
		return s.verify(dir, a.Generation, a.All)
	case "explain":
		var a struct {
			Dir string `json:"dir"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return failed("invalid arguments: " + err.Error())
		}
		dir, err := s.resolve(a.Dir)
		if err != nil {
			return failed(err.Error())
		}
		d, ok := advise.LastDecision(dir)
		if !ok {
			return failed("no plan in this directory: the controller has not published one yet")
		}
		return okResult(decision(d))
	default:
		return nil
	}
}

func (s *Server) verify(dir string, generation int64, all bool) *callResult {
	opts := verify.Options{Now: time.Now(), CheckContent: true}
	if all {
		sum, err := verify.Summarize(dir, generation, opts)
		if err != nil {
			return failed(err.Error())
		}
		rows := make([]any, 0, len(sum.Verdicts))
		for _, v := range sum.Verdicts {
			rows = append(rows, verdict(v))
		}
		return okResult(map[string]any{
			"generations": rows,
			"tally": map[string]any{
				"generations": len(sum.Receipts), "verified": sum.Tally.Verified,
				"broken": sum.Tally.Invalid, "unchecked": sum.Tally.Unchecked,
				"invalidRate": sum.Tally.InvalidRate(),
			},
		})
	}
	genDir := dir
	if generation > 0 {
		genDir = filepath.Join(dir, fmt.Sprintf("gen-%04d", generation))
	} else if sealed, ok, err := resume.NewestSealed(dir); err == nil && ok {
		genDir = sealed.Dir
	} else {
		return failed("no sealed generation in this directory")
	}
	return okResult(verdict(verify.Classify(genDir, generation, opts)))
}

type fileVerdict struct {
	Generation int64    `json:"generation,omitempty"`
	Step       int64    `json:"step,omitempty"`
	State      string   `json:"state"`
	Reason     string   `json:"reason,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	Extras     []string `json:"unlisted,omitempty"`
}

func verdict(v verify.Verdict) fileVerdict {
	return fileVerdict{
		Generation: v.Generation, Step: v.Step,
		State: string(v.State), Reason: string(v.Reason), Detail: v.Detail, Extras: v.Extras,
	}
}

type decisionView struct {
	Action  string   `json:"action"`
	Reason  string   `json:"reason,omitempty"`
	Why     string   `json:"why,omitempty"`
	Kind    string   `json:"kind"`
	Keep    int      `json:"keep"`
	Missing []string `json:"missing,omitempty"`
}

func decision(d policy.Decision) decisionView {
	kind := string(d.Kind)
	if kind == "" {
		kind = "full"
	}
	return decisionView{
		Action: string(d.Action), Reason: string(d.Reason), Why: d.Why,
		Kind: kind, Keep: d.KeepCopies, Missing: d.Missing,
	}
}

func (s *Server) resolve(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("dir is required")
	}
	if len(s.Roots) == 0 {
		return "", fmt.Errorf("no --root was given to ckptctl mcp")
	}
	candidate := dir
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.Roots[0], candidate)
	}
	candidate = filepath.Clean(candidate)
	if real, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = real
	}
	for _, root := range s.Roots {
		r := filepath.Clean(root)
		if real, err := filepath.EvalSymlinks(r); err == nil {
			r = real
		}
		if candidate == r || strings.HasPrefix(candidate, r+string(os.PathSeparator)) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is outside the roots this server may read", dir)
}

func okResult(v any) *callResult {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return failed(err.Error())
	}
	return &callResult{
		Content:           []content{{Type: "text", Text: string(raw)}},
		StructuredContent: v,
	}
}

func failed(msg string) *callResult {
	return &callResult{Content: []content{{Type: "text", Text: msg}}, IsError: true}
}

func tools() []tool {
	dirSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"dir": map[string]any{"type": "string", "description": "checkpoint directory"}},
		"required":   []string{"dir"},
	}
	verifySchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"dir":        map[string]any{"type": "string"},
			"generation": map[string]any{"type": "integer"},
			"all":        map[string]any{"type": "boolean"},
		},
		"required": []string{"dir"},
	}
	return []tool{
		{Name: "status", Description: "What the controller knows about one checkpoint directory.", InputSchema: dirSchema},
		{Name: "resumable", Description: "Can this be resumed: the verdict, the reason, and the missing rank.", InputSchema: dirSchema},
		{Name: "verify", Description: "Is what landed sound: shards present and readable, one generation or all.", InputSchema: verifySchema},
		{Name: "explain", Description: "Why the last decision was what it was, from the state file.", InputSchema: dirSchema},
		{Name: "storage", Description: "What this volume promises: local, shared-safe, shared-unsafe.", InputSchema: dirSchema},
		{Name: "evidence", Description: "One schema-1 bundle: status, resume, verify, attempts, storage, problems.", InputSchema: dirSchema},
	}
}

func okBytes(id json.RawMessage, result any) []byte {
	raw, _ := json.Marshal(response{JSONRPC: "2.0", ID: id, Result: result})
	return raw
}

func errorBytes(id json.RawMessage, code int, msg string) []byte {
	raw, _ := json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
	return raw
}
