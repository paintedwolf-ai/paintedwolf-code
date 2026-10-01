package logview

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func testDisplay() Display { return NewDisplay(DefaultConfig(), false) }

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	testutil.FailErr(t, "marshal fixture", err)
	return b
}

func sampleCalls(t *testing.T) []LLMRecord {
	t.Helper()
	return []LLMRecord{
		{
			Surface: "implement_investigate", AgentType: "coordinator", Model: "gemini",
			Iteration: 1, MaxIterations: 40, SessionID: "024e7449-1882-4470",
			ToolNames: []string{"read", "edit"},
			Usage:     &LLMUsage{PromptTokens: 1000, CompletionTokens: 20},
			Messages: []LLMMessage{
				{Role: "system", Content: mustMarshal(t, "# Coordinator\nrules")},
				{Role: "user", Content: mustMarshal(t, "fix the bug")},
			},
		},
		{
			Surface: "", AgentType: "command-verifier",
			Iteration: 5, SessionID: "744d113c-0000",
			Usage: &LLMUsage{PromptTokens: 500, CompletionTokens: 9},
			Messages: []LLMMessage{
				{Role: "assistant", Content: mustMarshal(t, ""), ToolCalls: []LLMToolCall{
					{Name: "update_progress", Args: mustMarshal(t, map[string]string{"content": "## Progress\n- [x] done"})},
				}},
				{Role: "tool", Content: mustMarshal(t, `{"status":"updated"}`)},
			},
		},
	}
}

func TestSelectCall(t *testing.T) {
	recs := sampleCalls(t)

	last, idx, err := SelectCall(recs, 0)
	testutil.FailErr(t, "select last", err)
	if idx != 2 || last.AgentType != "command-verifier" {
		t.Fatalf("expected last call #2 command-verifier, got #%d %s", idx, last.AgentType)
	}

	first, idx, err := SelectCall(recs, 1)
	testutil.FailErr(t, "select first", err)
	if idx != 1 || first.AgentType != "coordinator" {
		t.Fatalf("expected call #1 coordinator, got #%d %s", idx, first.AgentType)
	}

	if _, _, err := SelectCall(recs, 99); err == nil {
		t.Fatal("expected out-of-range error")
	}
	if _, _, err := SelectCall(nil, 0); err == nil {
		t.Fatal("expected empty-capture error")
	}
}

func TestRenderPromptContent(t *testing.T) {
	recs := sampleCalls(t)
	rec, idx, err := SelectCall(recs, 1)
	testutil.FailErr(t, "select", err)

	var buf bytes.Buffer
	err = testDisplay().RenderPrompt(&buf, rec, idx, PromptOptions{ShowTools: true})
	testutil.FailErr(t, "render prompt", err)
	out := buf.String()

	for _, want := range []string{
		"LLM call #1",
		"coordinator",
		"↑1,000 prompt",
		"┌─ system",
		"# Coordinator",
		"┌─ user",
		"fix the bug",
		"Available tools",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt render missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderPromptHideSystemAndToolCall(t *testing.T) {
	recs := sampleCalls(t)
	rec, idx, err := SelectCall(recs, 2)
	testutil.FailErr(t, "select", err)

	var buf bytes.Buffer
	err = testDisplay().RenderPrompt(&buf, rec, idx, PromptOptions{HideSystem: true})
	testutil.FailErr(t, "render", err)
	out := buf.String()

	if strings.Contains(out, "┌─ system") {
		t.Errorf("system block should be hidden\n%s", out)
	}
	if !strings.Contains(out, "→ update_progress") {
		t.Errorf("tool call not rendered\n%s", out)
	}
	if !strings.Contains(out, `"## Progress\n- [x] done"`) && !strings.Contains(out, "- [x] done") {
		t.Errorf("tool args not rendered\n%s", out)
	}
	if !strings.Contains(out, `{"status":"updated"}`) {
		t.Errorf("tool result not rendered\n%s", out)
	}
}

func TestExportPromptPlain(t *testing.T) {
	recs := sampleCalls(t)
	rec, _, err := SelectCall(recs, 2)
	testutil.FailErr(t, "select", err)

	out := ExportPromptPlain(testDisplay(), rec, PromptOptions{HideSystem: true})
	if strings.ContainsAny(out, "│┌━") {
		t.Fatalf("plain export must omit box drawing\n%s", out)
	}
	if !strings.Contains(out, "assistant\n") || !strings.Contains(out, "→ update_progress\n") {
		t.Fatalf("missing role/tool lines\n%s", out)
	}
	if strings.Contains(out, "system\n") {
		t.Fatalf("HideSystem should omit system\n%s", out)
	}
}

func TestRenderLLMListFiltersKeepGlobalIndex(t *testing.T) {
	recs := sampleCalls(t)
	var buf bytes.Buffer
	err := testDisplay().RenderLLMList(&buf, recs, Filter{Agent: "command-verifier"})
	testutil.FailErr(t, "render list", err)
	out := buf.String()

	if strings.Contains(out, "coordinator") {
		t.Errorf("coordinator should be filtered out\n%s", out)
	}
	if !strings.Contains(out, "#2") {
		t.Errorf("filtered row should keep global index #2\n%s", out)
	}
}

func TestRenderHTTPErrorsOnly(t *testing.T) {
	d := 3
	recs := []HTTPRecord{
		{Method: "GET", Path: "/v1/ok", Status: 200, DurationMS: &d},
		{Method: "POST", Path: "/v1/projects", Status: 405, DurationMS: &d},
	}
	var buf bytes.Buffer
	err := testDisplay().RenderHTTP(&buf, recs, Filter{Errors: true})
	testutil.FailErr(t, "render http", err)
	out := buf.String()

	if strings.Contains(out, "/v1/ok") {
		t.Errorf("2xx should be filtered with --errors\n%s", out)
	}
	if !strings.Contains(out, "405") || !strings.Contains(out, "/v1/projects") {
		t.Errorf("error row missing\n%s", out)
	}
}

func TestMessageTextVariants(t *testing.T) {
	if got := messageText(json.RawMessage(`"hello"`)); got != "hello" {
		t.Errorf("string content: got %q", got)
	}
	arr := json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)
	if got := messageText(arr); got != "a\nb" {
		t.Errorf("array content: got %q", got)
	}
	if got := messageText(json.RawMessage(`{"weird":1}`)); !strings.Contains(got, "weird") {
		t.Errorf("fallback content: got %q", got)
	}
}

func TestCleanTaskStripsMarker(t *testing.T) {
	in := "<!-- lycaon-worker-task-assignment:v1 -->\nRun the tests please"
	if got := cleanTask(in); got != "Run the tests please" {
		t.Errorf("cleanTask = %q", got)
	}
}

func TestSSEEnvelopeOp(t *testing.T) {
	env := SSEEnvelope{Data: json.RawMessage(`{"op":"patch","x":1}`)}
	if got := env.Op(); got != "patch" {
		t.Errorf("Op = %q", got)
	}
	if got := (SSEEnvelope{}).Op(); got != "" {
		t.Errorf("empty Op = %q", got)
	}
}

func TestHumanThousands(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 7429638: "7,429,638"}
	for in, want := range cases {
		if got := human(in); got != want {
			t.Errorf("human(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestWrapPlain(t *testing.T) {
	// word break at the last space within width
	got := wrapPlain("alpha beta gamma delta", 12)
	if len(got) < 2 || got[0] != "alpha beta" {
		t.Errorf("word wrap = %#v", got)
	}
	// hard break for an unbreakable token
	got = wrapPlain("aaaaaaaaaaaaaaaaaa", 6)
	for _, seg := range got {
		if len([]rune(seg)) > 6 {
			t.Errorf("segment %q exceeds width 6", seg)
		}
	}
}

func TestRenderPromptWrapsToWidth(t *testing.T) {
	long := strings.Repeat("word ", 60)
	rec := LLMRecord{Surface: "s", AgentType: "a", Messages: []LLMMessage{
		{Role: "user", Content: rawStr(t, long)},
	}}
	var buf bytes.Buffer
	d := NewDisplay(DefaultConfig(), false).WithWidth(40)
	if err := d.RenderPrompt(&buf, rec, 1, PromptOptions{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, ln := range strings.Split(buf.String(), "\n") {
		if w := len([]rune(ln)); w > 40 {
			t.Errorf("line exceeds width 40 (%d): %q", w, ln)
		}
	}
}

func TestAgentToolEvents(t *testing.T) {
	turn := LLMRecord{Messages: []LLMMessage{
		{Role: "user", Content: rawStr(t, "go")},
		{Role: "assistant", Content: rawStr(t, "I will run tests"), ToolCalls: []LLMToolCall{
			{Name: "command", Args: json.RawMessage(`{"cmd":"pytest"}`)},
		}},
		{Role: "tool", Content: rawStr(t, `{"exit":0}`)},
		{Role: "assistant", Content: rawStr(t, "now read the file"), ToolCalls: []LLMToolCall{
			{Name: "read", Args: json.RawMessage(`{"path":"a.py"}`)},
		}},
		{Role: "tool", Content: rawStr(t, "file body")},
	}}
	a := &Agent{Turns: []LLMRecord{turn}}

	evs := a.ToolEvents()
	if len(evs) != 2 {
		t.Fatalf("expected 2 tool events, got %d", len(evs))
	}
	if evs[0].Name != "command" || !strings.Contains(evs[0].Result, "exit") {
		t.Errorf("event 0 = %+v", evs[0])
	}
	if evs[1].Name != "read" || evs[1].Reasoning != "now read the file" || evs[1].Result != "file body" {
		t.Errorf("event 1 = %+v", evs[1])
	}
}

func TestRenderPromptFocusOffset(t *testing.T) {
	rec := LLMRecord{Surface: "s", AgentType: "a", Messages: []LLMMessage{
		{ID: "m1", Role: "system", Content: rawStr(t, strings.Repeat("system line\n", 20))},
		{ID: "m2", Role: "user", Content: rawStr(t, "the question")},
		{ID: "m3", Role: "assistant", Content: rawStr(t, "TARGET reasoning here")},
	}}
	var buf bytes.Buffer
	d := NewDisplay(DefaultConfig(), false)

	off := d.RenderPromptFocus(&buf, rec, 1, PromptOptions{}, "m3")
	if off <= 0 {
		t.Fatalf("focus offset for m3 should be a positive line, got %d", off)
	}
	lines := strings.Split(buf.String(), "\n")
	if off >= len(lines) || !strings.Contains(lines[off], "assistant") {
		t.Errorf("offset %d should point at the assistant block header; line = %q", off, lines[off])
	}
	// An absent id yields 0.
	if d.RenderPromptFocus(&buf, rec, 1, PromptOptions{}, "nope") != 0 {
		t.Error("absent focus id should return 0")
	}
}

func TestToolEventsAggregatesAcrossTurns(t *testing.T) {
	// turn 2 has compacted the read call out, but the command call is new there.
	a := &Agent{Turns: []LLMRecord{
		{Iteration: 1, Messages: []LLMMessage{
			{Role: "assistant", ToolCalls: []LLMToolCall{{Name: "read", ID: "r1"}}},
			{Role: "tool", Content: rawStr(t, "[read#1] {}")},
		}},
		{Iteration: 2, Messages: []LLMMessage{
			{Role: "assistant", ToolCalls: []LLMToolCall{{Name: "command", ID: "b1"}}},
			{Role: "tool", Content: rawStr(t, "[command#1] {}")},
		}},
	}}
	evs := a.ToolEvents()
	if ToolIndexByCallID(evs, "r1") < 0 || ToolIndexByCallID(evs, "b1") < 0 {
		t.Errorf("both tools across turns should be captured, got %+v", evs)
	}
	if len(evs) != 2 {
		t.Errorf("expected 2 distinct tools, got %d", len(evs))
	}
}

func TestRenderTools(t *testing.T) {
	a := &Agent{AgentType: "command-verifier", SessionID: "abc12345", Turns: []LLMRecord{{Messages: []LLMMessage{
		{Role: "assistant", Content: rawStr(t, "run it"), ToolCalls: []LLMToolCall{
			{Name: "command", Args: json.RawMessage(`{"cmd":"x"}`)},
		}},
		{Role: "tool", Content: rawStr(t, "ok")},
	}}}}
	var buf bytes.Buffer
	if err := testDisplay().RenderTools(&buf, a, a.ToolEvents()); err != nil {
		testutil.FailErr(t, "testDisplay failed", err)
	}
	out := buf.String()
	if !strings.Contains(out, "command-verifier") || !strings.Contains(out, "command") {
		t.Errorf("tools render = %q", out)
	}
}

func TestChildSessionsIn(t *testing.T) {
	rec := LLMRecord{Messages: []LLMMessage{
		{Role: "assistant", Content: rawStr(t, "")},
		{Role: "tool", Content: rawStr(t, `<task child_session_id="w1" state="complete"></task> and <task child_session_id="w2">`)},
		{Role: "tool", Content: rawStr(t, `<task child_session_id="w1">`)}, // duplicate
	}}
	got := ChildSessionsIn(rec)
	if len(got) != 2 || got[0] != "w1" || got[1] != "w2" {
		t.Errorf("ChildSessionsIn = %#v, want [w1 w2]", got)
	}
}

func TestDecodeJSONLSkipsBadLines(t *testing.T) {
	in := strings.NewReader(`{"method":"GET","path":"/a","status":200}` + "\n" +
		"not json\n" +
		`{"method":"POST","path":"/b","status":500}` + "\n")
	recs, err := ReadJSONL[HTTPRecord](in)
	testutil.FailErr(t, "decode", err)
	if len(recs) != 2 {
		t.Fatalf("expected 2 valid records, got %d", len(recs))
	}
	if recs[1].Path != "/b" {
		t.Errorf("second record = %q", recs[1].Path)
	}
}

func TestDisplayUnescapeHTML(t *testing.T) {
	raw := mustMarshal(t, "line1&#xA;quote&#34;end")
	plain := Display{}.text(raw)
	if !strings.Contains(plain, "&#xA;") {
		t.Errorf("default should keep entities: %q", plain)
	}
	d := Display{UnescapeHTML: true}
	if got := d.text(raw); got != "line1\nquote\"end" {
		t.Errorf("unescaped = %q", got)
	}
}

func TestDisplayTimeFormat(t *testing.T) {
	ts := time.Date(2026, 6, 22, 10, 21, 14, 0, time.Local)
	if got := (Display{}).Time(ts); !strings.HasPrefix(got, "10:21:14") {
		t.Errorf("clock format = %q", got)
	}
	if got := (Display{TimeFormat: "iso"}).Time(ts); !strings.HasPrefix(got, "2026-06-22T10:21:14") {
		t.Errorf("iso format = %q", got)
	}
}

func TestConfigGetSetRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.SetField("unescape_html", "true"); err != nil {
		t.Fatalf("set unescape_html: %v", err)
	}
	if !cfg.UnescapeHTML {
		t.Error("unescape_html not applied")
	}
	if v, _ := cfg.GetField("unescape_html"); v != "true" {
		t.Errorf("get unescape_html = %q", v)
	}
	if err := cfg.SetField("color", "purple"); err == nil {
		t.Error("expected validation error for bad color")
	}
	if err := cfg.SetField("nope", "x"); err == nil {
		t.Error("expected unknown-key error")
	}
}

func TestTailerYieldsAppendedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "http.jsonl")
	testutil.FailErr(t, "seed", os.WriteFile(path,
		[]byte(`{"method":"GET","path":"/a","status":200}`+"\n"), 0o600))

	tl := NewTailer[HTTPRecord](path, false) // start at end → only new rows
	if recs, err := tl.Poll(); err != nil || len(recs) != 0 {
		t.Fatalf("expected no initial rows, got %d (%v)", len(recs), err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	testutil.FailErr(t, "open append", err)
	_, _ = f.WriteString(`{"method":"POST","path":"/b","status":500}` + "\n")
	_, _ = f.WriteString(`{"method":"GET","path":"/c","status":` + "\n") // partial line
	testutil.FailErr(t, "close", f.Close())

	recs, err := tl.Poll()
	testutil.FailErr(t, "poll", err)
	if len(recs) != 1 || recs[0].Path != "/b" {
		t.Fatalf("expected only the complete /b row, got %+v", recs)
	}
}
