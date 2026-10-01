package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	logview "github.com/lycaon/lycaon/internal/logview"
	"github.com/lycaon/lycaon/internal/testutil"
)

// viewText returns the rendered view with ANSI (color + syntax highlighting)
// stripped, for stable substring assertions.
func viewText(m *Model) string { return ansi.Strip(m.View()) }

// writeCapture lays down a capture with a coordinator, one worker (with a derived
// outcome), their turns, and a couple of raw rows, returning its directory.
func writeCapture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"sessions.jsonl": strings.Join([]string{
			`{"session_id":"coord","agent_type":"coordinator","surface":"implement_investigate","task":"Tell me about this repo."}`,
			`{"session_id":"work1","parent_session_id":"coord","agent_type":"command-verifier","task":"\nTask mode:\n- mode: read\n- suggested paths:\n- tests\n"}`,
		}, "\n"),
		"llm-requests.jsonl": strings.Join([]string{
			`{"session_id":"coord","agent_type":"coordinator","surface":"implement_investigate","iteration":1,"usage":{"prompt_tokens":1000,"completion_tokens":5},"messages":[{"role":"system","content":"coordinator system prompt"},{"role":"user","content":"Tell me about this repo."}]}`,
			`{"session_id":"coord","agent_type":"coordinator","surface":"implement_investigate","iteration":2,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"I will check the board","tool_calls":[{"name":"pack_board","id":"x1","args":{"detail_level":"compact"}}]},{"role":"tool","content":"<task child_session_id=\"work1\" agent_type=\"command-verifier\" state=\"complete\"><report_json>{\"leg_status\":\"complete\"}</report_json></task>"}]}`,
			`{"session_id":"work1","agent_type":"command-verifier","iteration":1,"usage":{"prompt_tokens":500,"completion_tokens":2},"messages":[{"role":"user","content":"Worker task started"}]}`,
		}, "\n"),
		"http-requests.jsonl": `{"method":"POST","path":"/v1/projects","status":405}`,
		"sse-events.jsonl":    `{"topic":"board","session_id":"coord","envelope":{"data":{"op":"patch"}}}`,
		"sidecar.log": strings.Join([]string{
			`time=2026-06-22T13:31:11.499-07:00 level=WARN msg="heads up" key=val`,
			`time=2026-06-22T13:31:12.000-07:00 level=DEBUG msg="noise"`,
		}, "\n"),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// newSized opens the model directly on a fixture capture (dir given), so it starts
// at the session level without scanning the real captures directory.
func newSized(t *testing.T) *Model {
	t.Helper()
	m, err := New(logview.DefaultConfig(), writeCapture(t))
	if err != nil {
		t.Fatalf("new model: %v", err)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	m = next.(*Model)
	// The session view defaults to the combined timeline; these tests navigate the
	// agent tree, so switch to it (t toggles tree/timeline).
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	return m
}

func enter(m *Model)  { m.Update(tea.KeyMsg{Type: tea.KeyEnter}) }
func escape(m *Model) { m.Update(tea.KeyMsg{Type: tea.KeyEsc}) }

func TestCapturesPickerListsAndOpens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, ".config", "paintedwolf", "debug", "sessions")
	mk := func(name, task string) {
		d := filepath.Join(base, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
		sess := `{"session_id":"c","agent_type":"coordinator","task":"` + task + `"}` + "\n"
		llm := `{"session_id":"c","agent_type":"coordinator","iteration":1,"messages":[{"role":"user","content":"` + task + `"}]}` + "\n"
		_ = os.WriteFile(filepath.Join(d, "sessions.jsonl"), []byte(sess), 0o600)
		_ = os.WriteFile(filepath.Join(d, "llm-requests.jsonl"), []byte(llm), 0o600)
	}
	mk("20260101T000000Z", "First task")
	mk("20260102T000000Z", "Second task")

	m, err := New(logview.DefaultConfig(), "") // empty dir → picker
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = next.(*Model)
	if m.level != levelCaptures {
		t.Fatalf("empty dir should start at the captures picker, got %v", m.level)
	}
	v := m.View()
	if !strings.Contains(v, "First task") || !strings.Contains(v, "Second task") {
		t.Fatalf("picker should list both captures\n%s", v)
	}

	m.list.Select(0) // newest first → "Second task"
	enter(m)
	if m.level != levelSession {
		t.Fatalf("enter should open a session, got %v", m.level)
	}
	if !strings.Contains(m.View(), "Second task") {
		t.Errorf("opened the wrong session\n%s", m.View())
	}
	escape(m)
	if m.level != levelCaptures {
		t.Errorf("esc should return to the captures picker, got %v", m.level)
	}
}

func TestLiveFeedStreamDrillResume(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	debug := filepath.Join(home, ".config", "paintedwolf", "debug")
	dir := filepath.Join(debug, "sessions", "20260622T203110Z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	llmPath := filepath.Join(dir, "llm-requests.jsonl")
	_ = os.WriteFile(filepath.Join(dir, "sessions.jsonl"),
		[]byte(`{"session_id":"c","agent_type":"coordinator","task":"Do"}`+"\n"), 0o600)
	// Baseline record present before we attach (its backlog must be skipped).
	_ = os.WriteFile(llmPath,
		[]byte(`{"session_id":"c","agent_type":"coordinator","messages":[{"role":"user","content":"go"}]}`+"\n"), 0o600)
	if err := os.Symlink(dir, filepath.Join(debug, "latest")); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	appendLine := func(line string) {
		f, err := os.OpenFile(llmPath, os.O_APPEND|os.O_WRONLY, 0o600)
		testutil.FailErr(t, "os.OpenFile failed", err)
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}

	m, err := New(logview.DefaultConfig(), "")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = next.(*Model)

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}) // f → live feed
	if m.level != levelLive || !m.follow {
		t.Fatalf("f should open the live feed, got level=%v follow=%v", m.level, m.follow)
	}
	if !strings.Contains(m.View(), "Live") {
		t.Errorf("live header missing\n%s", m.View())
	}

	// First appended record establishes the per-session baseline (no events).
	appendLine(`{"session_id":"c","agent_type":"coordinator","iteration":2,"messages":[{"id":"u1","role":"user","content":"go"},{"id":"a1","role":"assistant","tool_calls":[{"name":"read","id":"r1"}]},{"id":"t1","role":"tool","content":"x"}]}`)
	m.livePoll()
	// Next record adds a new tool call → a streamed event.
	appendLine(`{"session_id":"c","agent_type":"coordinator","iteration":3,"messages":[{"id":"u1","role":"user","content":"go"},{"id":"a1","role":"assistant","tool_calls":[{"name":"read","id":"r1"}]},{"id":"t1","role":"tool","content":"x"},{"id":"a2","role":"assistant","tool_calls":[{"name":"grep","id":"g1"}]},{"id":"t2","role":"tool","content":"[grep#1] {\"matches\":[1,2]}"}]}`)
	m.livePoll()
	if !strings.Contains(viewText(m), "grep") {
		t.Errorf("live feed should stream the new tool call\n%s", viewText(m))
	}

	// Drill into the selected event — the grep tool — exactly, not the whole list.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.level != levelToolDetail {
		t.Fatalf("Enter on a tool event should open that tool's detail, got %v", m.level)
	}
	if m.agent == nil || m.agent.SessionID != "c" {
		t.Fatalf("drilled into the wrong agent: %+v", m.agent)
	}
	if m.toolIdx >= len(m.tools) || m.tools[m.toolIdx].Name != "grep" {
		t.Fatalf("should land on the exact grep call, got tool idx %d", m.toolIdx)
	}
	if !m.follow {
		t.Error("the feed should keep following (buffering) while drilled in")
	}

	// Esc resumes back at the live feed.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.level != levelLive {
		t.Fatalf("esc should return to the live feed, got %v", m.level)
	}
	if !m.follow {
		t.Error("still following after returning from a drill")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}) // f → stop
	if m.follow || m.level == levelLive {
		t.Errorf("f should exit the live feed, got level=%v follow=%v", m.level, m.follow)
	}
}

// liveModel sets up a model following a live capture, returning it and an appender
// for streaming more transcript records.
func liveModel(t *testing.T) (*Model, func(string)) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	debug := filepath.Join(home, ".config", "paintedwolf", "debug")
	dir := filepath.Join(debug, "sessions", "20260622T203110Z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	llmPath := filepath.Join(dir, "llm-requests.jsonl")
	_ = os.WriteFile(filepath.Join(dir, "sessions.jsonl"),
		[]byte(`{"session_id":"c","agent_type":"coordinator","task":"Do"}`+"\n"), 0o600)
	_ = os.WriteFile(llmPath,
		[]byte(`{"session_id":"c","agent_type":"coordinator","iteration":1,"messages":[{"id":"u1","role":"user","content":"go"}]}`+"\n"), 0o600)
	if err := os.Symlink(dir, filepath.Join(debug, "latest")); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	appendLine := func(line string) {
		f, err := os.OpenFile(llmPath, os.O_APPEND|os.O_WRONLY, 0o600)
		testutil.FailErr(t, "os.OpenFile failed", err)
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
	m, err := New(logview.DefaultConfig(), "")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = next.(*Model)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}) // live
	return m, appendLine
}

func TestLivePauseIndependentOfDrill(t *testing.T) {
	m, appendLine := liveModel(t)
	space := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")}
	say := func(it int) {
		appendLine(fmt.Sprintf(`{"session_id":"c","agent_type":"coordinator","iteration":%d,"messages":[{"id":"u1","role":"user","content":"go"},{"id":"a%d","role":"assistant","content":"step %d"}]}`, it, it, it))
		m.livePoll()
	}
	say(2) // baseline
	say(3) // streams a say event
	last := func() bool { return m.liveList.Index() == len(m.liveList.Items())-1 }

	// Drill while live, return — should stay following (not paused).
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.livePaused {
		t.Fatal("drilling in must not pause the feed")
	}
	say(4)
	if !last() {
		t.Errorf("an un-paused feed should keep following the newest event")
	}

	// Explicit pause: cursor must hold while events keep arriving.
	m.Update(space)
	if !m.livePaused {
		t.Fatal("space should pause")
	}
	m.liveList.Select(0)
	before := m.liveList.Index()
	say(5)
	if m.liveList.Index() != before {
		t.Errorf("a paused feed must not move the selection")
	}

	// Drilling while paused returns still paused.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.livePaused {
		t.Error("drilling while paused should stay paused")
	}

	// Space resumes and snaps to the newest.
	m.Update(space)
	if m.livePaused || !last() {
		t.Errorf("space should resume following at the newest event")
	}
}

func TestLiveNavigationDoesNotPause(t *testing.T) {
	m, appendLine := liveModel(t)
	say := func(it int) {
		appendLine(fmt.Sprintf(`{"session_id":"c","agent_type":"coordinator","iteration":%d,"messages":[{"id":"u1","role":"user","content":"go"},{"id":"a%d","role":"assistant","content":"step %d"}]}`, it, it, it))
		m.livePoll()
	}
	for i := 2; i <= 6; i++ {
		say(i)
	}

	// Moving the selection around must never pause the feed.
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.livePaused {
		t.Fatal("navigation must not pause the feed")
	}

	// While browsing (cursor off the bottom), incoming events must not yank it.
	at := m.liveList.Index()
	say(7)
	if m.liveList.Index() != at {
		t.Errorf("a browsed (non-bottom) selection should not be pulled to the newest event")
	}
	if m.livePaused {
		t.Error("still must not be paused while browsing a live feed")
	}

	// Pause is explicit only.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if !m.livePaused {
		t.Error("space should pause")
	}
}

func TestLiveDrillFindsNewlyStreamedTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	debug := filepath.Join(home, ".config", "paintedwolf", "debug")
	dir := filepath.Join(debug, "sessions", "20260622T203110Z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	llmPath := filepath.Join(dir, "llm-requests.jsonl")
	_ = os.WriteFile(filepath.Join(dir, "sessions.jsonl"),
		[]byte(`{"session_id":"c","agent_type":"coordinator","task":"Do"}`+"\n"), 0o600)
	_ = os.WriteFile(llmPath,
		[]byte(`{"session_id":"c","agent_type":"coordinator","iteration":1,"messages":[{"id":"u1","role":"user","content":"go"}]}`+"\n"), 0o600)
	if err := os.Symlink(dir, filepath.Join(debug, "latest")); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	appendLine := func(line string) {
		f, err := os.OpenFile(llmPath, os.O_APPEND|os.O_WRONLY, 0o600)
		testutil.FailErr(t, "os.OpenFile failed", err)
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
	m, err := New(logview.DefaultConfig(), "")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = next.(*Model)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}) // live

	// it2 baselines the engine; it3 streams command; drill it, then it4 streams read.
	appendLine(`{"session_id":"c","agent_type":"coordinator","iteration":2,"messages":[{"id":"u1","role":"user","content":"go"},{"id":"a1","role":"assistant","tool_calls":[{"name":"grep","id":"g1"}]},{"id":"t1","role":"tool","content":"[grep#1] {}"}]}`)
	m.livePoll()
	appendLine(`{"session_id":"c","agent_type":"coordinator","iteration":3,"messages":[{"id":"u1","role":"user","content":"go"},{"id":"a1","role":"assistant","tool_calls":[{"name":"grep","id":"g1"}]},{"id":"t1","role":"tool","content":"[grep#1] {}"},{"id":"a2","role":"assistant","tool_calls":[{"name":"command","id":"b1"}]},{"id":"t2","role":"tool","content":"[command#1] {\"ExitCode\":0}"}]}`)
	m.livePoll()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // drill command (first load of the capture)
	if m.level != levelToolDetail || m.tools[m.toolIdx].Name != "command" {
		t.Fatalf("first drill should land on command, got level=%v", m.level)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // back to the feed

	// The capture grows with a new tool after the navigator was already loaded.
	appendLine(`{"session_id":"c","agent_type":"coordinator","iteration":4,"messages":[{"id":"u1","role":"user","content":"go"},{"id":"a1","role":"assistant","tool_calls":[{"name":"grep","id":"g1"}]},{"id":"t1","role":"tool","content":"[grep#1] {}"},{"id":"a2","role":"assistant","tool_calls":[{"name":"command","id":"b1"}]},{"id":"t2","role":"tool","content":"[command#1] {}"},{"id":"a3","role":"assistant","tool_calls":[{"name":"read","id":"rd1"}]},{"id":"t3","role":"tool","content":"[read#1] {\"content\":\"1\\n2\"}"}]}`)
	m.livePoll()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // drill the just-streamed read
	if m.level != levelToolDetail {
		t.Fatalf("drilling a newly-streamed tool should open its detail, got %v", m.level)
	}
	if m.tools[m.toolIdx].Name != "read" {
		t.Errorf("should land on the read tool, got %q", m.tools[m.toolIdx].Name)
	}
}

func TestSessionDefaultsToTimelineAndDrills(t *testing.T) {
	// newSized toggles to the tree, so open fresh to get the default view.
	m, err := New(logview.DefaultConfig(), writeCapture(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	m = next.(*Model)

	if !m.sessionTimeline {
		t.Fatal("a session should default to the combined timeline")
	}
	v := viewText(m)
	if !strings.Contains(v, "timeline") {
		t.Errorf("breadcrumb should show timeline mode\n%s", v)
	}
	// The timeline holds events (a coordinator session start, the worker spawn, etc.).
	if len(m.sessionEvents) == 0 {
		t.Fatal("expected a combined event timeline")
	}
	it, ok := m.list.SelectedItem().(navItem)
	if !ok || it.kind != kindEvent {
		t.Fatalf("timeline rows should be events, got %#v", m.list.SelectedItem())
	}

	// Drilling an event lands inside its agent (not just the whole list).
	m.list.Select(0)
	enter(m)
	if m.agent == nil {
		t.Fatalf("drilling a timeline event should enter an agent, level=%v", m.level)
	}

	// t toggles to the agent tree.
	escape(m)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if m.sessionTimeline {
		t.Error("t should switch to the agent tree")
	}
	if !strings.Contains(viewText(m), "command-verifier") {
		t.Errorf("agent tree should list the workers\n%s", viewText(m))
	}
}

func TestTimelineSayDrillScrollsToMessage(t *testing.T) {
	dir := t.TempDir()
	bigSystem := strings.Repeat("policy clause ", 300) // wraps to many lines
	_ = os.WriteFile(filepath.Join(dir, "sessions.jsonl"),
		[]byte(`{"session_id":"c","agent_type":"coordinator","task":"Do"}`+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "llm-requests.jsonl"), []byte(strings.Join([]string{
		`{"session_id":"c","agent_type":"coordinator","iteration":1,"messages":[{"id":"s1","role":"system","content":"` + bigSystem + `"},{"id":"u1","role":"user","content":"go"}]}`,
		`{"session_id":"c","agent_type":"coordinator","iteration":2,"messages":[{"id":"s1","role":"system","content":"` + bigSystem + `"},{"id":"u1","role":"user","content":"go"},{"id":"a1","role":"assistant","content":"my specific reasoning"}]}`,
	}, "\n")+"\n"), 0o600)

	m, err := New(logview.DefaultConfig(), dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 18})
	m = next.(*Model)

	sayIdx := -1
	for i, e := range m.sessionEvents {
		if e.Kind == logview.EventSay {
			sayIdx = i
			break
		}
	}
	if sayIdx < 0 {
		t.Fatal("expected a say event in the timeline")
	}

	m.list.Select(sayIdx)
	enter(m)
	if m.level != levelTurn {
		t.Fatalf("drilling a say event should open its turn, got %v", m.level)
	}
	// The assistant message sits below a long system block, so the pane must have
	// scrolled past the top to land on it.
	if m.detail.YOffset == 0 {
		t.Errorf("detail should scroll to the message, but stayed at the top")
	}
}

// A single iteration can span several LLM records (different surfaces). Drilling a
// say event must land on the record that actually holds the message — not the first
// record sharing its iteration, which would leave the pane stuck at the top.
func TestTimelineDrillLandsOnMessageNotIteration(t *testing.T) {
	dir := t.TempDir()
	bigSystem := strings.Repeat("policy clause ", 300)
	_ = os.WriteFile(filepath.Join(dir, "sessions.jsonl"),
		[]byte(`{"session_id":"c","agent_type":"coordinator","task":"Do"}`+"\n"), 0o600)
	// Two iteration-2 records: the first surface has no assistant message; the second
	// holds the say. Locating by iteration would pick the first and miss the message.
	_ = os.WriteFile(filepath.Join(dir, "llm-requests.jsonl"), []byte(strings.Join([]string{
		`{"session_id":"c","agent_type":"coordinator","surface":"a","iteration":1,"messages":[{"id":"s1","role":"system","content":"` + bigSystem + `"},{"id":"u1","role":"user","content":"go"}]}`,
		`{"session_id":"c","agent_type":"coordinator","surface":"helper","iteration":2,"messages":[{"id":"s1","role":"system","content":"` + bigSystem + `"},{"id":"u1","role":"user","content":"go"}]}`,
		`{"session_id":"c","agent_type":"coordinator","surface":"a","iteration":2,"messages":[{"id":"s1","role":"system","content":"` + bigSystem + `"},{"id":"u1","role":"user","content":"go"},{"id":"a1","role":"assistant","content":"my specific reasoning"}]}`,
	}, "\n")+"\n"), 0o600)

	m, err := New(logview.DefaultConfig(), dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 18})
	m = next.(*Model)

	sayIdx := -1
	for i, e := range m.sessionEvents {
		if e.Kind == logview.EventSay {
			sayIdx = i
		}
	}
	if sayIdx < 0 {
		t.Fatal("expected a say event in the timeline")
	}
	m.list.Select(sayIdx)
	enter(m)
	if m.level != levelTurn {
		t.Fatalf("drilling a say event should open its turn, got %v", m.level)
	}
	has := false
	for _, msg := range m.agent.Turns[m.turnIdx].Messages {
		if msg.ID == "a1" {
			has = true
		}
	}
	if !has {
		t.Errorf("landed on turn %d, which does not contain the message — wrong surface", m.turnIdx)
	}
	if m.detail.YOffset == 0 {
		t.Errorf("detail should scroll to the message below the system block, but stayed at the top")
	}
}

func TestSessionLevelShowsAgentsAndOutcome(t *testing.T) {
	m := newSized(t)
	v := m.View()
	for _, want := range []string{"Tell me about this repo.", "coordinator", "command-verifier", "complete", "HTTP requests"} {
		if !strings.Contains(v, want) {
			t.Errorf("session view missing %q\n%s", want, v)
		}
	}
}

func TestDrillFromSessionToPrompt(t *testing.T) {
	m := newSized(t)
	if m.level != levelSession {
		t.Fatalf("expected to start at session level, got %v", m.level)
	}

	m.list.Select(0) // coordinator
	enter(m)
	if m.level != levelAgent {
		t.Fatalf("enter on agent should open the timeline, got %v", m.level)
	}
	if v := m.View(); !strings.Contains(v, "turn 1") {
		t.Errorf("agent timeline missing turns\n%s", v)
	}

	m.list.Select(0) // turn 1
	enter(m)
	if m.level != levelTurn {
		t.Fatalf("enter on turn should open the prompt, got %v", m.level)
	}
	if v := viewText(m); !strings.Contains(v, "coordinator system prompt") {
		t.Errorf("turn detail missing prompt content\n%s", v)
	}

	escape(m)
	if m.level != levelAgent {
		t.Errorf("esc from turn should return to agent, got %v", m.level)
	}
	escape(m)
	if m.level != levelSession {
		t.Errorf("esc from agent should return to session, got %v", m.level)
	}
}

func TestRawStreamsReachableFromSession(t *testing.T) {
	m := newSized(t)
	// session items: agents(0,1) · Problems(2) · HTTP(3) · SSE(4) · Den perf(5) · Backend log(6)
	m.list.Select(3)
	enter(m)
	if m.level != levelRawList || m.rawKind != streamHTTP {
		t.Fatalf("expected HTTP raw list, got level=%v kind=%v", m.level, m.rawKind)
	}
	if v := m.View(); !strings.Contains(v, "/v1/projects") {
		t.Errorf("HTTP list missing request\n%s", v)
	}
	enter(m)
	if m.level != levelRawDetail {
		t.Fatalf("enter on a request should open detail, got %v", m.level)
	}
}

func TestChildJumpFromCoordinatorTurn(t *testing.T) {
	m := newSized(t)
	m.list.Select(0) // coordinator
	enter(m)
	m.list.Select(1) // turn 2 carries the task() result for work1
	enter(m)
	if m.level != levelTurn {
		t.Fatalf("expected turn detail, got %v", m.level)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	if m.level != levelAgent || m.agent.AgentType != "command-verifier" {
		t.Fatalf("w should jump into the worker, got level=%v agent=%q", m.level, m.agent.AgentType)
	}
	escape(m)
	if m.level != levelTurn {
		t.Errorf("esc after a child jump should return to the coordinator turn, got %v", m.level)
	}
}

func TestDetailSearchFindsAndCounts(t *testing.T) {
	m := newSized(t)
	m.list.Select(0) // coordinator
	enter(m)
	m.list.Select(0) // turn 1 (has the system prompt)
	enter(m)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.searching {
		t.Fatal(`"/" should enter search mode`)
	}
	for _, r := range "system" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.searching {
		t.Error("Enter should leave search input")
	}
	if len(m.matches) == 0 {
		t.Errorf("search for 'system' should find the system prompt line")
	}
}

func TestToolViewToggleAndDetail(t *testing.T) {
	m := newSized(t)
	m.list.Select(0) // coordinator
	enter(m)
	if it := m.list.SelectedItem().(navItem); it.kind != kindTurn {
		t.Fatalf("agent timeline should default to turns, got kind %v", it.kind)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}) // swap to tools
	if !m.toolView {
		t.Fatal("t should enable the tool view")
	}
	it, ok := m.list.SelectedItem().(navItem)
	if !ok || it.kind != kindTool {
		t.Fatalf("tool view should list tools, got %#v", m.list.SelectedItem())
	}
	if v := m.View(); !strings.Contains(v, "pack_board") {
		t.Errorf("tool row missing the tool name\n%s", v)
	}

	enter(m) // open the tool detail
	if m.level != levelToolDetail {
		t.Fatalf("enter on a tool should open the tool detail, got %v", m.level)
	}
	if v := m.View(); !strings.Contains(v, "pack_board") || !strings.Contains(v, "result") {
		t.Errorf("tool detail missing call/result\n%s", v)
	}
	escape(m)
	if m.level != levelAgent || !m.toolView {
		t.Errorf("esc should return to the agent's tool view, got level=%v toolView=%v", m.level, m.toolView)
	}
}

func TestProblemsViewNavigatesToTarget(t *testing.T) {
	m := newSized(t)
	// session items: agents(0,1) · Problems(2) · HTTP(3) · SSE(4) · Den perf(5) · Backend log(6)
	m.list.Select(2)
	enter(m)
	if m.level != levelProblems {
		t.Fatalf("expected the problems list, got %v", m.level)
	}
	it, ok := m.list.SelectedItem().(navItem)
	if !ok || it.kind != kindProblem {
		t.Fatalf("problems list should hold problem rows, got %#v", m.list.SelectedItem())
	}
	enter(m) // the 405 → its HTTP detail
	if m.level != levelRawDetail {
		t.Fatalf("a problem should jump to its location, got %v", m.level)
	}
	if !strings.Contains(m.View(), "/v1/projects") {
		t.Errorf("did not land on the offending request\n%s", m.View())
	}
}

func TestBackendLogViewAndAllToggle(t *testing.T) {
	m := newSized(t)
	m.list.Select(6) // Backend log
	enter(m)
	if m.level != levelLog {
		t.Fatalf("expected the backend log, got %v", m.level)
	}
	if got := len(m.visibleLogs()); got != 1 {
		t.Errorf("warn/err filter should show 1 line, got %d", got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if got := len(m.visibleLogs()); got != 2 {
		t.Errorf("show-all should reveal the DEBUG line too, got %d", got)
	}
	if !strings.Contains(m.View(), "heads up") {
		t.Errorf("log message missing\n%s", m.View())
	}
}

func TestPromptDiffToggle(t *testing.T) {
	m := newSized(t)
	m.list.Select(0) // coordinator
	enter(m)
	m.list.Select(1) // turn 2
	enter(m)
	if m.diffMode {
		t.Fatal("diff off by default")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if !m.diffMode {
		t.Fatal("d should toggle diff mode")
	}
	if v := m.View(); !strings.Contains(v, "diff: turn 1 → turn 2") {
		t.Errorf("diff header missing\n%s", v)
	}
}

func TestDetailSiblingNavigation(t *testing.T) {
	m := newSized(t)
	m.list.Select(0) // coordinator (2 turns)
	enter(m)
	m.list.Select(0) // turn 1 detail
	enter(m)
	if m.level != levelTurn || m.turnIdx != 0 {
		t.Fatalf("expected turn 1 detail, got level=%v idx=%d", m.level, m.turnIdx)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRight}) // → next turn, still zoomed in
	if m.level != levelTurn {
		t.Fatalf("should stay in the detail pane, got %v", m.level)
	}
	if m.turnIdx != 1 {
		t.Fatalf("right should advance to turn 2, got %d", m.turnIdx)
	}
	if m.list.Index() != 1 {
		t.Errorf("parent list cursor should follow, got %d", m.list.Index())
	}
	if !strings.Contains(m.View(), "pack_board") {
		t.Errorf("turn 2 content not shown after sibling step\n%s", m.View())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRight}) // clamp at the last turn
	if m.turnIdx != 1 {
		t.Errorf("should clamp at the last turn, got %d", m.turnIdx)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyLeft}) // ← back to turn 1
	if m.turnIdx != 0 {
		t.Errorf("left should step back to turn 1, got %d", m.turnIdx)
	}
}

func TestTurnDetailSystemToggle(t *testing.T) {
	m := newSized(t)
	m.list.Select(0) // coordinator (its turn 1 has a system message)
	enter(m)
	m.list.Select(0)
	enter(m) // turn detail
	withSystem := viewText(m)
	if !strings.Contains(withSystem, "coordinator system prompt") {
		t.Fatalf("system shown by default\n%s", withSystem)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if strings.Contains(viewText(m), "coordinator system prompt") {
		t.Error("toggling s should hide the system prompt")
	}
}
