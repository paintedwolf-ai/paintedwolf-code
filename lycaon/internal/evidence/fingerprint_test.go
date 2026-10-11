package evidence_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNormalizeCitationPath(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "internal", "foo.go")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Dir(src), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(src, []byte("package foo\n"), 0o644))

	cases := []struct {
		token string
		want  string
		ok    bool
	}{
		{"internal/foo.go", "internal/foo.go", true},
		{"./internal/foo.go", "internal/foo.go", true},
		{"../escape.go", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := evidence.NormalizeCitationPath(root, tc.token)
		if ok != tc.ok {
			t.Fatalf("evidence.NormalizeCitationPath(%q) ok=%v want %v", tc.token, ok, tc.ok)
		}
		if got != tc.want {
			t.Fatalf("evidence.NormalizeCitationPath(%q) = %q want %q", tc.token, got, tc.want)
		}
	}

	abs := src
	got, ok := evidence.NormalizeCitationPath(root, abs)
	if !ok || got != "internal/foo.go" {
		t.Fatalf("absolute path normalize = %q ok=%v", got, ok)
	}
}

func TestBuildLedgerFromTranscript_readAndGrep(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "pkg", "main.go")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Dir(src), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(src, []byte("package main\n"), 0o644))

	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}],"receipt":{"tool":"grep"}}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: "read", ID: "c1", Args: map[string]any{"path": "pkg/main.go"}},
				{Name: "grep", ID: "c2", Args: map[string]any{"path": ".", "pattern": "main"}},
			},
		},
		{Role: api.MessageRoleTool, Content: "package main\n", ToolResult: &api.ToolResult{Content: "package main\n", Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}

	ev := ledgertest.BuildFromMessages(root, msgs)
	if !evidence.PathObserved(ev, "pkg/main.go") {
		t.Fatalf("expected pkg/main.go in evidence, got %v", evidence.ObservedPathsSorted(ev))
	}
	if _, ok := evidence.ResolveHandle(ev, "grep#1"); !ok {
		t.Fatal("expected grep#1 handle in ledger")
	}
}

func TestBuildLedgerFromTranscript_commandStdoutIgnoresTracebackTokens(t *testing.T) {
	root := t.TempDir()
	traceback := `$PATH (most) -c -m 1 8 <module>
File "/Users/example/work/space/game.py", line 8, in <module>
  import foo`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: "command", ID: "c1", Args: map[string]any{"command": "python game.py"}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    traceback,
			ToolResult: &api.ToolResult{Content: traceback, Outcome: api.ToolResultOutcomeCompleted},
		},
	}

	ev := ledgertest.BuildFromMessages(root, msgs)
	for _, junk := range []string{"$PATH", "(most", "-c", "-m", "1", "8", "<module>", "File", "No", "import"} {
		if evidence.PathObserved(ev, junk) {
			t.Fatalf("traceback token %q must not become observed path; paths=%v", junk, evidence.ObservedPathsSorted(ev))
		}
	}
}

func TestBuildLedgerFromTranscript_commandJSONIndexesFileCommandPath(t *testing.T) {
	root := t.TempDir()
	doc := filepath.Join(root, "docs", "coordinator-execution-modes.md")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Dir(doc), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(doc, []byte("# Coordinator execution modes\n\n**SSOT** for orchestrate.\n"), 0o644))

	body := `{"stages":[{"command":"file docs/coordination.md","exit_code":0}],"exit_code":0,"tail":"docs/coordination.md: Unicode text, UTF-8 text\n","ok":true,"timed_out":false}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: "command", ID: "c1", Args: map[string]any{"command": "file docs/coordination.md"}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    body,
			ToolResult: &api.ToolResult{Content: body, Outcome: api.ToolResultOutcomeCompleted},
		},
	}
	ev := ledgertest.BuildFromMessages(root, msgs)
	if !evidence.PathObserved(ev, "docs/coordination.md") {
		t.Fatalf("expected file command path in evidence, got %v", evidence.ObservedPathsSorted(ev))
	}
	for _, junk := range evidence.ObservedPathsSorted(ev) {
		if strings.Contains(junk, "ExitCode") || strings.Contains(junk, `"`) {
			t.Fatalf("command JSON debris must not become observed path: %q in %v", junk, evidence.ObservedPathsSorted(ev))
		}
	}
	rec, ok := evidence.ResolveHandle(ev, "command#1")
	if !ok || rec.Path != "docs/coordination.md" {
		t.Fatalf("command#1 record = %+v ok=%v", rec, ok)
	}
}

func TestBuildLedgerFromTranscript_commandJSONIndexesPythonOpenPath(t *testing.T) {
	root := t.TempDir()
	doc := filepath.Join(root, "docs", "sample.md")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Dir(doc), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(doc, []byte("# Sample\n"), 0o644))

	cmd := `python3 -c "d=open('docs/sample.md','rb').read(); print(d[:20].decode())"`
	body := `{"stages":[{"command":` + strconv.Quote(cmd) + `,"exit_code":0}],"exit_code":0,"tail":"# Sample\n","ok":true,"timed_out":false}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: "command", ID: "c1", Args: map[string]any{"command": cmd}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    body,
			ToolResult: &api.ToolResult{Content: body, Outcome: api.ToolResultOutcomeCompleted},
		},
	}
	ev := ledgertest.BuildFromMessages(root, msgs)
	if !evidence.PathObserved(ev, "docs/sample.md") {
		t.Fatalf("expected python open() path in evidence, got %v", evidence.ObservedPathsSorted(ev))
	}
}

func TestBuildLedgerFromTranscript_commandStdoutRegistersGitStatusPath(t *testing.T) {
	root := t.TempDir()
	out := " M internal/session/worker_cycle.go"
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: "command", ID: "c1", Args: map[string]any{"command": "git status --short"}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    out,
			ToolResult: &api.ToolResult{Content: out, Outcome: api.ToolResultOutcomeCompleted},
		},
	}

	ev := ledgertest.BuildFromMessages(root, msgs)
	if !evidence.PathObserved(ev, "internal/session/worker_cycle.go") {
		t.Fatalf("expected git status path in evidence, got %v", evidence.ObservedPathsSorted(ev))
	}
}

func TestBuildLedgerFromTranscript_skipsRejectedTool(t *testing.T) {
	root := t.TempDir()
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: "read", ID: "c1", Args: map[string]any{"path": "missing.go"}},
			},
		},
		{
			Role:    api.MessageRoleTool,
			Content: "Rejected: READ_NOT_FOUND\nCode: READ_NOT_FOUND",
			ToolResult: &api.ToolResult{
				Content: "Rejected: READ_NOT_FOUND\nCode: READ_NOT_FOUND",
				Outcome: api.ToolResultOutcomeRejected,
				Codes:   []string{"READ_NOT_FOUND"},
			},
		},
	}
	ev := ledgertest.BuildFromMessages(root, msgs)
	if len(evidence.ObservedPathsSorted(ev)) != 0 {
		t.Fatalf("rejected tool should not contribute paths: %v", evidence.ObservedPathsSorted(ev))
	}
	if len(ev.Handles) != 0 {
		t.Fatalf("rejected tool should not assign handles: %v", ev.Handles)
	}
}

func TestBuildRecordDeletePaths(t *testing.T) {
	root := t.TempDir()
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{
				Name: "delete",
				ID:   "d1",
				Args: map[string]any{"paths": []any{"a.txt", "b.txt"}},
			}},
		},
		{
			Role:    api.MessageRoleTool,
			Content: `{"deleted":["a.txt","b.txt"]}`,
			ToolResult: &api.ToolResult{
				Content: `{"deleted":["a.txt","b.txt"]}`,
				Outcome: api.ToolResultOutcomeCompleted,
			},
		},
	}
	ev := ledgertest.BuildFromMessages(root, msgs)
	paths := evidence.ObservedPathsSorted(ev)
	if len(paths) != 2 || paths[0] != "a.txt" || paths[1] != "b.txt" {
		t.Fatalf("delete paths = %v", paths)
	}
}

// A structural rewrite authors bytes like any other write tool, so the paths it
// declares are indexed for citation.
func TestBuildRecordCodeRewritePaths(t *testing.T) {
	root := t.TempDir()
	for name, args := range map[string]map[string]any{
		"single path": {"path": "a.go", "pattern": "foo($A)", "rewrite": "bar($A)"},
		"codemod set": {"paths": []any{"a.go", "b.go"}, "pattern": "foo($A)", "rewrite": "bar($A)"},
	} {
		t.Run(name, func(t *testing.T) {
			msgs := []api.Message{
				{
					Role: api.MessageRoleAssistant,
					ToolCalls: []api.ToolCall{{
						Name: "code_rewrite",
						ID:   "cr1",
						Args: args,
					}},
				},
				{
					Role:    api.MessageRoleTool,
					Content: `{"rewritten":1}`,
					ToolResult: &api.ToolResult{
						Content: `{"rewritten":1}`,
						Outcome: api.ToolResultOutcomeCompleted,
					},
				},
			}
			ev := ledgertest.BuildFromMessages(root, msgs)
			paths := evidence.ObservedPathsSorted(ev)
			if len(paths) == 0 {
				t.Fatal("code_rewrite must index the paths it declared")
			}
			if paths[0] != "a.go" {
				t.Fatalf("paths = %v want a.go indexed", paths)
			}
		})
	}
}

func TestBuildRecord_fingerprintTruthTable(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name  string
		tool  string
		body  string
		path  string
		shape string
		kind  string
	}{
		{
			name:  "git_diff_stat",
			tool:  "git_diff",
			body:  `{"available":true,"files":[{"path":"src/changed.go","insertions":1,"deletions":0}]}`,
			path:  "src/changed.go",
			shape: evidence.ShapeCommand,
			kind:  "git",
		},
		{
			name:  "scan_query",
			tool:  "scan_query",
			body:  `{"scan_id":"s1","findings":[{"rule_id":"r1","level":"high","message":"issue","locations":[{"uri":"pkg/main.go"}]}],"total_match":1}`,
			path:  "pkg/main.go",
			shape: evidence.ShapeArtifact,
			kind:  "scan",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := evidence.BuildEvidenceRecord(root, tc.tool, nil, tc.body)
			if rec.Kind != tc.kind || rec.Shape != tc.shape {
				t.Fatalf("rec = %+v want kind=%s shape=%s", rec, tc.kind, tc.shape)
			}
			if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: tc.path}); !ok {
				t.Fatalf("path %q not indexed on %s record", tc.path, tc.tool)
			}
		})
	}
}

func TestReadRecordsCoverTheLinesTheReadReturned(t *testing.T) {
	symbol := evidence.BuildEvidenceRecord("", "read", map[string]any{"path": "pkg/a.go", "symbol": "load"},
		`{"path":"pkg/a.go","mode":"symbol","symbol":"load","count":1,"start_line":40,"end_line":52,"content":"40\tfunc load() {\n"}`)
	if len(symbol.LineRanges) != 1 || symbol.LineRanges[0].Start != 40 || symbol.LineRanges[0].End != 52 {
		t.Fatalf("symbol read ranges = %+v, want 40-52", symbol.LineRanges)
	}
	outline := evidence.BuildEvidenceRecord("", "read", map[string]any{"path": "pkg/a.go"},
		`{"path":"pkg/a.go","mode":"outline","symbols":[{"kind":"func","name":"main","line":1}],"total_lines":500}`)
	if len(outline.LineRanges) != 0 {
		t.Fatalf("outline read ranges = %+v, want none", outline.LineRanges)
	}
}

func TestRecordFromToolResult_HTTPAndBrowserURLs(t *testing.T) {
	httpRec := evidence.BuildEvidenceRecord("", "http_request", map[string]any{
		"url": "http://127.0.0.1:8765/api/status",
	}, `[http_response#1]
{"status":200,"final_url":"http://127.0.0.1:8765/api/status"}`)
	urls := evidence.ObservedURLsForRecord(httpRec)
	found := false
	for _, u := range urls {
		if u == "http://127.0.0.1:8765/api/status" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("http_request URL not observed: got %v", urls)
	}

	pageRec := evidence.BuildEvidenceRecord("", "capture_page", map[string]any{
		"url": "http://127.0.0.1:8765/",
	}, `[page#1]
{"artifact_id":"abc","caption":"dashboard","final_url":"http://127.0.0.1:8765/"}`)
	pageURLs := evidence.ObservedURLsForRecord(pageRec)
	foundPage := false
	for _, u := range pageURLs {
		if u == "http://127.0.0.1:8765/" {
			foundPage = true
			break
		}
	}
	if !foundPage {
		t.Fatalf("capture_page URL not observed: got %v", pageURLs)
	}
}

func TestCommandOutputURLsRemainOutcomeEvidenceNotPathsOrWebObservations(t *testing.T) {
	root := t.TempDir()
	body := `{"stages":[{"command":"gh issue create","exit_code":0}],"exit_code":0,"tail":"https://github.com/owner/repo/issues/64\nmailto:user@example.test\n","ok":true}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "command", ID: "url-output", Args: map[string]any{"command": "gh issue create"}}}},
		{Role: api.MessageRoleTool, Content: body, ToolResult: &api.ToolResult{Content: body, Outcome: api.ToolResultOutcomeCompleted}},
	}
	ev := ledgertest.BuildFromMessages(root, msgs)
	if paths := evidence.ObservedPathsSorted(ev); len(paths) != 0 {
		t.Fatalf("URL output became paths: %v", paths)
	}
	if urls := evidence.ObservedURLsFromEvidence(ev); len(urls) != 0 {
		t.Fatalf("output became web observation: %v", urls)
	}
	if _, ok := evidence.ResolveHandle(ev, "command#1"); !ok {
		t.Fatal("command outcome handle missing")
	}
}
