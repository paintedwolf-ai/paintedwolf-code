// Package tui is the interactive session browser over a debug capture. It reuses
// internal/logview for all data access and rendering, so the TUI and the plain CLI
// present identical content. The browser is a drill-down navigator: Sessions (all
// captures) → one session's agents → turns → prompt, with the raw HTTP/SSE streams
// as a secondary view.
package tui

import (
	"bytes"
	"fmt"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	logview "github.com/lycaon/lycaon/internal/logview"
)

// level is the current depth in the drill-down.
type level int

const (
	levelCaptures   level = iota // home: every captured session
	levelSession                 // agent tree + raw-stream entries for one capture
	levelAgent                   // turns (or tools) of the current agent
	levelTurn                    // prompt transcript of the current turn
	levelToolDetail              // one tool call + result
	levelRawList                 // HTTP or SSE list
	levelRawDetail               // detail of one HTTP/SSE row
	levelProblems                // session-wide problems list
	levelLog                     // backend slog list
	levelLogDetail               // one slog line
	levelLive                    // full-screen live event feed (tail -f)
)

// itemKind tags what Enter does for a list row.
type itemKind int

const (
	kindCapture itemKind = iota
	kindAgent
	kindRawHTTP
	kindRawSSE
	kindRawDenPerf
	kindProblemsMenu
	kindLogMenu
	kindTurn
	kindTool
	kindHTTPRow
	kindSSERow
	kindDenPerfRow
	kindProblem
	kindLogRow
	kindEvent
)

type navItem struct {
	title  string
	filter string
	kind   itemKind
	idx    int
}

func (i navItem) FilterValue() string { return i.filter }

// streamKind selects which raw stream the secondary HTTP/SSE view shows.
type streamKind int

const (
	streamHTTP streamKind = iota
	streamSSE
	streamDenPerf
)

func (k streamKind) title() string {
	switch k {
	case streamSSE:
		return "SSE events"
	case streamDenPerf:
		return "Den perf"
	default:
		return "HTTP requests"
	}
}

func clip(s string, n int) string {
	if n <= 0 {
		return s
	}
	return runeclamp.Clamp(s, n)
}

// singleLineDelegate renders each row on a single line with a selection gutter.
type singleLineDelegate struct{}

func (singleLineDelegate) Height() int                         { return 1 }
func (singleLineDelegate) Spacing() int                        { return 0 }
func (singleLineDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (singleLineDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(navItem)
	if !ok {
		return
	}
	gutter := "  "
	if index == m.Index() {
		gutter = cursorStyle.Render("❯ ")
	}
	fmt.Fprint(w, gutter+it.title)
}

// frame remembers a level we can return to with Esc.
type frame struct {
	level   level
	agent   *logview.Agent
	rawKind streamKind
	turnIdx int
	rawIdx  int
	toolIdx int
	cursor  int
}

// Model is the bubbletea state for the session browser.
type Model struct {
	capt  *logview.Capture
	cfg   logview.Config
	color logview.Display
	plain logview.Display

	captures    []logview.CaptureSummary // lazily loaded picker rows
	captureName string                   // the open capture's name, for the breadcrumb

	tree     *logview.SessionTree
	httpRecs []logview.HTTPRecord
	sse      []logview.SSERecord
	denPerf  []logview.DenPerfRecord

	llm      []logview.LLMRecord
	sessions []logview.SessionRecord

	level    level
	agent    *logview.Agent
	turnIdx  int
	rawKind  streamKind
	rawIdx   int
	toolView bool                // agent timeline shows tools instead of turns
	tools    []logview.ToolEvent // the current agent's tool calls
	toolIdx  int
	diffMode bool // turn detail shows the diff vs the previous turn

	problems   []logview.Problem
	logRecs    []logview.LogRecord
	logIdx     int
	logShowAll bool // backend log shows all levels, not just warn/error

	sessionTimeline bool            // session view shows the combined event timeline (vs the agent tree)
	sessionEvents   []logview.Event // the combined timeline for the open capture

	list            list.Model
	detail          viewport.Model
	detailLines     []string // ANSI-free detail lines, for in-pane search
	detailFocusMsg  string   // scroll the turn detail to this message id (one-shot)
	turnFocusOffset int      // computed line offset of detailFocusMsg
	stack           []frame

	searching bool
	search    string
	matches   []int
	matchPos  int

	// `|` pipe: empty Enter copies the detail buffer; a command feeds it to the shell.
	piping    bool
	pipeCmd   string
	status    string
	statusGen int

	showSystem bool
	follow     bool // the live feed is active

	// live event feed (tail -f) — a selectable list you can pause, drill into, resume
	liveList     list.Model
	liveEvents   []logview.Event
	livePaused   bool // feed frozen for browsing; independent of drilling in
	liveCapt     *logview.Capture
	liveEngine   *logview.FollowEngine
	liveTailLLM  *logview.Tailer[logview.LLMRecord]
	liveTailSess *logview.Tailer[logview.SessionRecord]
	liveDir      string

	width, height int
	ready         bool
}

// New builds the initial model. An empty dir starts at the captures picker; a
// non-empty dir resolves and opens that capture directly at its session view, with
// the picker seeded as the parent so Esc returns to it.
func New(cfg logview.Config, dir string) (*Model, error) {
	m := &Model{
		cfg:             cfg,
		color:           logview.NewDisplay(cfg, true),
		plain:           logview.NewDisplay(cfg, false),
		level:           levelCaptures,
		showSystem:      !cfg.HideSystem,
		sessionTimeline: true, // combined narrative is the default session view
	}
	m.list = list.New(nil, singleLineDelegate{}, 80, 20)
	m.list.SetShowTitle(false)
	m.list.SetShowHelp(false)
	m.list.SetShowStatusBar(false)
	m.list.DisableQuitKeybindings()

	if dir != "" {
		capt, err := logview.Resolve(dir)
		if err != nil {
			return nil, err
		}
		if err := m.openCapture(capt); err != nil {
			return nil, err
		}
		m.level = levelSession
		m.stack = append(m.stack, frame{level: levelCaptures})
	}
	return m, nil
}

// openCapture loads a capture's data and builds its session tree. It is the lazy
// load run when a capture is picked.
func (m *Model) openCapture(capt *logview.Capture) error {
	var err error
	if m.llm, err = capt.LLM(); err != nil {
		return err
	}
	if m.sessions, err = capt.Sessions(); err != nil {
		return err
	}
	if m.httpRecs, err = capt.HTTP(); err != nil {
		return err
	}
	if m.sse, err = capt.SSE(); err != nil {
		return err
	}
	if m.denPerf, err = capt.DenPerf(); err != nil {
		return err
	}
	if m.logRecs, err = capt.LogRecords(); err != nil {
		return err
	}
	m.capt = capt
	m.captureName = filepath.Base(capt.Dir)
	m.tree = logview.BuildSessionTree(m.sessions, m.llm)
	m.problems = logview.CollectProblems(m.tree, m.httpRecs, m.denPerf)
	m.sessionEvents = logview.BuildEventLog(m.sessions, m.llm, m.denPerf)
	return nil
}

// ensureCaptures lazily loads the picker rows the first time the captures level is
// shown, so a --dir launch doesn't scan every capture up front.
func (m *Model) ensureCaptures() {
	if m.captures != nil {
		return
	}
	if caps, err := logview.ListCaptureSummaries(); err == nil {
		m.captures = caps
	}
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) Init() tea.Cmd {
	if m.cfg.Follow {
		return tick() // first WindowSize enters the live feed
	}
	return nil
}

// items builds the list rows for the current level.
func (m *Model) items() []list.Item {
	switch m.level {
	case levelCaptures:
		m.ensureCaptures()
		out := make([]list.Item, len(m.captures))
		for i, s := range m.captures {
			out[i] = navItem{title: m.color.CaptureRow(s), filter: m.plain.CaptureRow(s), kind: kindCapture, idx: i}
		}
		return out
	case levelSession:
		if m.sessionTimeline {
			out := make([]list.Item, len(m.sessionEvents))
			for i, ev := range m.sessionEvents {
				out[i] = navItem{title: m.color.EventLine(ev), filter: m.plain.EventLine(ev), kind: kindEvent, idx: i}
			}
			return out
		}
		out := make([]list.Item, 0, len(m.tree.Agents)+2)
		for i, a := range m.tree.Agents {
			out = append(out, navItem{title: m.color.AgentRow(a), filter: m.plain.AgentRow(a), kind: kindAgent, idx: i})
		}
		probTitle := m.color.Dim(fmt.Sprintf("Problems (%d) ›", len(m.problems)))
		if len(m.problems) > 0 {
			probTitle = m.color.Yellow(fmt.Sprintf("⚠ Problems (%d) ›", len(m.problems)))
		}
		warn := logview.CountLogsAtLeast(m.logRecs, warnRank)
		out = append(out,
			navItem{title: probTitle, filter: "problems", kind: kindProblemsMenu},
			navItem{title: m.color.Dim(fmt.Sprintf("HTTP requests (%d) ›", len(m.httpRecs))), filter: "http requests", kind: kindRawHTTP},
			navItem{title: m.color.Dim(fmt.Sprintf("SSE events (%d) ›", len(m.sse))), filter: "sse events", kind: kindRawSSE},
			navItem{title: m.color.Dim(fmt.Sprintf("Den perf (%d) ›", len(m.denPerf))), filter: "den perf", kind: kindRawDenPerf},
			navItem{title: m.color.Dim(fmt.Sprintf("Backend log (%d warn/err) ›", warn)), filter: "backend log", kind: kindLogMenu},
		)
		return out
	case levelAgent:
		if m.toolView {
			out := make([]list.Item, len(m.tools))
			for i, e := range m.tools {
				out[i] = navItem{title: m.color.ToolRow(i+1, e), filter: m.plain.ToolRow(i+1, e), kind: kindTool, idx: i}
			}
			return out
		}
		out := make([]list.Item, 0, len(m.agent.Turns))
		for i, tn := range m.agent.Turns {
			out = append(out, navItem{title: m.color.TurnRow(i+1, tn), filter: m.plain.TurnRow(i+1, tn), kind: kindTurn, idx: i})
		}
		return out
	case levelRawList:
		return m.rawItems()
	case levelProblems:
		out := make([]list.Item, len(m.problems))
		for i, p := range m.problems {
			out[i] = navItem{title: m.color.ProblemRow(p), filter: m.plain.ProblemRow(p), kind: kindProblem, idx: i}
		}
		return out
	case levelLog:
		recs := m.visibleLogs()
		out := make([]list.Item, len(recs))
		for i, r := range recs {
			out[i] = navItem{title: m.color.LogRow(r), filter: m.plain.LogRow(r), kind: kindLogRow, idx: i}
		}
		return out
	default:
		return nil
	}
}

const warnRank = 2 // warn/error and above

// visibleLogs is the backend-log slice for the current filter (warn/error, or all).
func (m *Model) visibleLogs() []logview.LogRecord {
	if m.logShowAll {
		return m.logRecs
	}
	return logview.FilterLogs(m.logRecs, warnRank)
}

func (m *Model) rawItems() []list.Item {
	switch m.rawKind {
	case streamHTTP:
		out := make([]list.Item, len(m.httpRecs))
		for i, r := range m.httpRecs {
			out[i] = navItem{title: m.color.HTTPRow(r), filter: m.plain.HTTPRow(r), kind: kindHTTPRow, idx: i}
		}
		return out
	case streamDenPerf:
		out := make([]list.Item, len(m.denPerf))
		for i, r := range m.denPerf {
			out[i] = navItem{title: m.color.DenPerfRow(r), filter: m.plain.DenPerfRow(r), kind: kindDenPerfRow, idx: i}
		}
		return out
	default:
		out := make([]list.Item, len(m.sse))
		for i, r := range m.sse {
			out[i] = navItem{title: m.color.SSERow(r), filter: m.plain.SSERow(r), kind: kindSSERow, idx: i}
		}
		return out
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.SetSize(msg.Width, m.bodyHeight())
		if m.liveList.Items() != nil || m.level == levelLive {
			m.liveList.SetSize(msg.Width, m.bodyHeight())
		}
		if !m.ready {
			m.detail = viewport.New(msg.Width, m.bodyHeight())
			// Content soft-wraps to width, so disable horizontal scroll and let
			// h/← mean "back" instead.
			m.detail.KeyMap.Left.SetEnabled(false)
			m.detail.KeyMap.Right.SetEnabled(false)
			m.ready = true
			m.reload(false)
			m.list.Select(0)
			if m.cfg.Follow {
				m.enterLive()
			}
		} else {
			m.detail.Width = msg.Width
			m.detail.Height = m.bodyHeight()
			if m.isDetailLevel() {
				m.refreshDetail()
			}
		}
		return m, nil

	case tickMsg:
		if m.follow {
			m.livePoll()
			return m, tick()
		}
		return m, nil

	case clearStatusMsg:
		if msg.gen == m.statusGen {
			m.status = ""
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.level == levelLive {
		var cmd tea.Cmd
		m.liveList, cmd = m.liveList.Update(msg)
		return m, cmd
	}
	if m.isDetailLevel() {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.level == levelLive {
		return m.handleLiveKey(msg)
	}
	if m.isDetailLevel() {
		return m.handleDetailKey(msg)
	}
	if m.list.SettingFilter() {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "backspace", "left", "h":
		m.pop()
		return m, nil
	case "enter", "right", "l", "o":
		m.descend()
		return m, nil
	case "f":
		// Sequenced, not inlined: m is a pointer, but keeping the mutation
		// ahead of the return states the ordering outright.
		cmd := m.toggleLive()
		return m, cmd
	case "t":
		if m.level == levelAgent {
			m.toolView = !m.toolView
			m.reload(true)
			return m, nil
		}
		if m.level == levelSession {
			m.sessionTimeline = !m.sessionTimeline
			m.reload(true)
			return m, nil
		}
	case "a":
		if m.level == levelLog {
			m.logShowAll = !m.logShowAll
			m.reload(true)
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *Model) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.searching {
		return m.handleSearchKey(msg)
	}
	if m.piping {
		return m.handlePipeKey(msg)
	}
	switch msg.String() {
	case "q", "esc", "backspace":
		m.pop()
		return m, nil
	case "right", "l":
		m.detailSibling(1)
		return m, nil
	case "left", "h":
		m.detailSibling(-1)
		return m, nil
	case "g", "home":
		m.detail.GotoTop()
		return m, nil
	case "G", "end":
		m.detail.GotoBottom()
		return m, nil
	case "/":
		m.searching = true
		m.search = ""
		m.status = ""
		return m, nil
	case "|":
		m.startPipe()
		return m, nil
	case "n":
		m.stepMatch(1)
		return m, nil
	case "N":
		m.stepMatch(-1)
		return m, nil
	case "s":
		if m.level == levelTurn {
			m.showSystem = !m.showSystem
			m.refreshDetail()
		}
		return m, nil
	case "d":
		if m.level == levelTurn {
			m.diffMode = !m.diffMode
			m.refreshDetail()
			m.detail.GotoTop()
		}
		return m, nil
	case "w":
		if m.level == levelTurn {
			m.jumpToChild()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg) // space/b page, d/u half, j/k line
	return m, cmd
}

func (m *Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.searching = false
		m.runSearch()
	case "esc":
		m.clearSearch()
	case "backspace":
		if m.search != "" {
			m.search = m.search[:len(m.search)-1]
		}
	default:
		if len(msg.Runes) > 0 {
			m.search += string(msg.Runes)
		}
	}
	return m, nil
}

func (m *Model) runSearch() {
	m.matches = nil
	m.matchPos = 0
	q := strings.ToLower(strings.TrimSpace(m.search))
	if q == "" {
		return
	}
	for i, ln := range m.detailLines {
		if strings.Contains(strings.ToLower(ln), q) {
			m.matches = append(m.matches, i)
		}
	}
	if len(m.matches) > 0 {
		m.detail.SetYOffset(m.matches[0])
	}
}

func (m *Model) stepMatch(dir int) {
	if len(m.matches) == 0 {
		return
	}
	m.matchPos = (m.matchPos + dir + len(m.matches)) % len(m.matches)
	m.detail.SetYOffset(m.matches[m.matchPos])
}

func (m *Model) clearSearch() {
	m.searching = false
	m.search = ""
	m.matches = nil
	m.matchPos = 0
}

// jumpToChild navigates from a coordinator turn's task() call into the worker
// session it spawned, pushing a frame so Esc returns to the turn.
func (m *Model) jumpToChild() {
	for _, id := range logview.ChildSessionsIn(m.agent.Turns[m.turnIdx]) {
		if child, ok := m.tree.Find(id); ok && child != m.agent {
			m.push()
			m.clearSearch()
			m.agent = child
			m.level = levelAgent
			m.reload(true)
			return
		}
	}
}

// currentTurnHasChild reports whether the open turn spawned a reachable worker.
func (m *Model) currentTurnHasChild() bool {
	if m.level != levelTurn {
		return false
	}
	for _, id := range logview.ChildSessionsIn(m.agent.Turns[m.turnIdx]) {
		if child, ok := m.tree.Find(id); ok && child != m.agent {
			return true
		}
	}
	return false
}

func (m *Model) isDetailLevel() bool {
	return m.level == levelTurn || m.level == levelToolDetail ||
		m.level == levelRawDetail || m.level == levelLogDetail
}

// descend opens the selected row one level deeper.
func (m *Model) descend() {
	it, ok := m.list.SelectedItem().(navItem)
	if !ok {
		return
	}
	switch it.kind {
	case kindCapture:
		capt, err := logview.Resolve(m.captures[it.idx].Dir)
		if err != nil {
			return
		}
		if err := m.openCapture(capt); err != nil {
			return
		}
		m.push()
		m.level = levelSession
		m.reload(true)
	case kindAgent:
		m.push()
		m.agent = m.tree.Agents[it.idx]
		m.tools = m.agent.ToolEvents()
		m.level = levelAgent
		m.reload(true)
	case kindRawHTTP:
		m.push()
		m.rawKind = streamHTTP
		m.level = levelRawList
		m.reload(true)
	case kindRawSSE:
		m.push()
		m.rawKind = streamSSE
		m.level = levelRawList
		m.reload(true)
	case kindRawDenPerf:
		m.push()
		m.rawKind = streamDenPerf
		m.level = levelRawList
		m.reload(true)
	case kindProblemsMenu:
		m.push()
		m.level = levelProblems
		m.reload(true)
	case kindLogMenu:
		m.push()
		m.level = levelLog
		m.reload(true)
	case kindProblem:
		m.openProblem(m.problems[it.idx])
	case kindEvent:
		if it.idx < len(m.sessionEvents) {
			m.drillEvent(m.sessionEvents[it.idx])
		}
	case kindLogRow:
		m.push()
		m.logIdx = it.idx
		m.level = levelLogDetail
		m.clearSearch()
		m.refreshDetail()
		m.detail.GotoTop()
	case kindTurn:
		m.push()
		m.turnIdx = it.idx
		m.level = levelTurn
		m.diffMode = false
		m.clearSearch()
		m.refreshDetail()
		m.detail.GotoTop()
	case kindTool:
		m.push()
		m.toolIdx = it.idx
		m.level = levelToolDetail
		m.clearSearch()
		m.refreshDetail()
		m.detail.GotoTop()
	case kindHTTPRow, kindSSERow, kindDenPerfRow:
		m.push()
		m.rawIdx = it.idx
		m.level = levelRawDetail
		m.clearSearch()
		m.refreshDetail()
		m.detail.GotoTop()
	}
}

// openProblem jumps from the problems list to the exact place a problem occurred.
func (m *Model) openProblem(p logview.Problem) {
	if p.Kind == logview.ProblemHTTP {
		m.push()
		m.rawKind = streamHTTP
		m.rawIdx = p.HTTPIndex
		m.level = levelRawDetail
		m.clearSearch()
		m.refreshDetail()
		m.detail.GotoTop()
		return
	}
	if p.Kind == logview.ProblemStall {
		m.push()
		m.rawKind = streamDenPerf
		m.rawIdx = p.StallIndex
		m.level = levelRawDetail
		m.clearSearch()
		m.refreshDetail()
		m.detail.GotoTop()
		return
	}
	agent, ok := m.tree.Find(p.AgentID)
	if !ok {
		return
	}
	m.push()
	m.agent = agent
	m.tools = agent.ToolEvents()
	m.clearSearch()
	switch {
	case p.ToolIndex >= 0:
		m.toolView = true
		m.toolIdx = p.ToolIndex
		m.level = levelToolDetail
		m.refreshDetail()
		m.detail.GotoTop()
	case p.TurnIndex >= 0:
		m.turnIdx = p.TurnIndex
		m.level = levelTurn
		m.diffMode = false
		m.refreshDetail()
		m.detail.GotoTop()
	default: // worker outcome → its timeline
		m.level = levelAgent
		m.reload(true)
	}
}

func (m *Model) push() {
	m.stack = append(m.stack, frame{
		level: m.level, agent: m.agent, rawKind: m.rawKind,
		turnIdx: m.turnIdx, rawIdx: m.rawIdx, toolIdx: m.toolIdx, cursor: m.list.Index(),
	})
}

func (m *Model) pop() {
	if len(m.stack) == 0 {
		return
	}
	f := m.stack[len(m.stack)-1]
	m.stack = m.stack[:len(m.stack)-1]
	m.level, m.agent, m.rawKind = f.level, f.agent, f.rawKind
	m.turnIdx, m.rawIdx, m.toolIdx = f.turnIdx, f.rawIdx, f.toolIdx
	if m.agent != nil {
		m.tools = m.agent.ToolEvents()
	}
	m.clearSearch()
	switch {
	case m.level == levelLive:
		// the live feed list persists across the drill; nothing to rebuild
	case m.isDetailLevel():
		m.refreshDetail()
	default:
		m.reload(false)
		m.list.Select(f.cursor)
	}
}

// reload rebuilds the list for the current level; resetCursor selects the top.
func (m *Model) reload(resetCursor bool) {
	idx := m.list.Index()
	m.list.SetItems(m.items())
	if resetCursor {
		m.list.Select(0)
	} else if idx < len(m.list.Items()) {
		m.list.Select(idx)
	}
}

// detailSibling steps to the previous/next item in the underlying list while
// staying zoomed into the detail pane — next tool, next turn, next request, etc.
// It keeps the parent list cursor in sync so backing out lands on the right row.
func (m *Model) detailSibling(delta int) {
	var idx *int
	var count int
	switch m.level {
	case levelTurn:
		idx, count = &m.turnIdx, len(m.agent.Turns)
	case levelToolDetail:
		idx, count = &m.toolIdx, len(m.tools)
	case levelRawDetail:
		idx, count = &m.rawIdx, m.rawCount()
	case levelLogDetail:
		idx, count = &m.logIdx, len(m.visibleLogs())
	default:
		return
	}
	next := clampIdx(*idx+delta, count)
	if next == *idx {
		return
	}
	*idx = next
	m.list.Select(next)
	m.clearSearch()
	m.refreshDetail()
	m.detail.GotoTop()
}

func (m *Model) rawCount() int {
	switch m.rawKind {
	case streamHTTP:
		return len(m.httpRecs)
	case streamDenPerf:
		return len(m.denPerf)
	default:
		return len(m.sse)
	}
}

func clampIdx(i, n int) int {
	if n == 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i > n-1 {
		return n - 1
	}
	return i
}

// renderDetail produces the current detail content with the given display (color
// for the pane, plain for building the searchable line index).
func (m *Model) renderDetail(d logview.Display) string {
	var buf bytes.Buffer
	switch m.level {
	case levelTurn:
		if m.diffMode {
			d.RenderTurnDiff(&buf, m.agent, m.turnIdx)
		} else {
			m.turnFocusOffset = d.RenderPromptFocus(&buf, m.agent.Turns[m.turnIdx], m.turnIdx+1,
				logview.PromptOptions{HideSystem: !m.showSystem}, m.detailFocusMsg)
		}
	case levelToolDetail:
		if m.toolIdx < len(m.tools) {
			d.ToolDetail(&buf, m.tools[m.toolIdx])
		}
	case levelLogDetail:
		recs := m.visibleLogs()
		if m.logIdx < len(recs) {
			d.LogDetail(&buf, recs[m.logIdx])
		}
	case levelRawDetail:
		switch m.rawKind {
		case streamHTTP:
			d.HTTPDetail(&buf, m.httpRecs[m.rawIdx])
		case streamDenPerf:
			d.DenPerfDetail(&buf, m.denPerf[m.rawIdx])
		default:
			d.SSEDetail(&buf, m.sse[m.rawIdx])
		}
	case levelCaptures, levelSession, levelAgent, levelRawList, levelProblems, levelLog, levelLive:
	}
	return buf.String()
}

// refreshDetail re-renders the detail pane at the current width and rebuilds the
// ANSI-free line index used for search. When a turn was opened focused on a specific
// message (from a timeline event), it scrolls to that message once.
func (m *Model) refreshDetail() {
	w := m.detail.Width
	m.detail.SetContent(m.renderDetail(m.color.WithWidth(w)))
	m.detailLines = strings.Split(m.renderDetail(m.plain.WithWidth(w)), "\n")
	switch {
	case m.search != "":
		m.runSearch()
	case m.detailFocusMsg != "" && m.level == levelTurn:
		m.detail.SetYOffset(max(0, m.turnFocusOffset-1))
		m.detailFocusMsg = "" // one-shot: don't re-jump on later re-renders
	}
}

// toggleLive enters or leaves the live event feed.
func (m *Model) toggleLive() tea.Cmd {
	if m.level == levelLive {
		m.exitLive()
		return nil
	}
	m.enterLive()
	return tick()
}

// enterLive opens the full-screen live feed: a selectable list of streamed events.
// Space pauses/resumes; Enter drills in (independent of pause); scrolling up pauses.
func (m *Model) enterLive() {
	m.push()
	m.level = levelLive
	m.follow = true
	m.livePaused = false
	m.liveEvents = nil
	m.liveList = list.New(nil, singleLineDelegate{}, m.width, m.bodyHeight())
	m.liveList.SetShowTitle(false)
	m.liveList.SetShowHelp(false)
	m.liveList.SetShowStatusBar(false)
	m.liveList.DisableQuitKeybindings()
	m.liveDir = ""
	m.attachLive()
}

func (m *Model) exitLive() {
	m.follow = false
	m.pop()
}

// attachLive (re)points the feed at the newest capture, starting its tailers at the
// end so only new events stream — like tail -f.
func (m *Model) attachLive() {
	newest, err := logview.NewestCapture()
	if err != nil {
		m.liveCapt = nil
		m.liveTailSess = nil
		return
	}
	m.liveCapt = newest
	m.liveDir = newest.Dir
	m.liveEngine = logview.NewFollowEngine()
	m.liveTailLLM = logview.NewTailer[logview.LLMRecord](newest.LLMPath, false)
	m.liveTailSess = logview.NewTailer[logview.SessionRecord](newest.SessionsPath, false)
}

// liveFollowing reports whether the feed is live (auto-scrolling), as opposed to
// explicitly paused. It is independent of drilling in.
func (m *Model) liveFollowing() bool { return !m.livePaused }

// livePoll appends newly-captured events to the feed, re-attaching if the sidecar
// rotated, and keeps the selection pinned to the newest event unless paused — so you
// can drill in and return to a still-live feed.
func (m *Model) livePoll() {
	if newest, err := logview.NewestCapture(); err == nil && newest.Dir != m.liveDir {
		m.attachLive()
	}
	if m.liveTailSess == nil {
		return
	}
	var events []logview.Event
	if recs, err := m.liveTailSess.Poll(); err == nil {
		events = append(events, m.liveEngine.SessionEvents(recs)...)
	}
	if recs, err := m.liveTailLLM.Poll(); err == nil {
		events = append(events, m.liveEngine.LLMEvents(recs)...)
	}
	if len(events) == 0 {
		return
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })

	// Follow the newest event only when not paused AND the cursor is already at the
	// bottom — so browsing back through a live feed isn't yanked to the end, and
	// navigation never has to flip the pause state.
	n := len(m.liveList.Items())
	atBottom := n == 0 || m.liveList.Index() >= n-1
	for _, ev := range events {
		i := len(m.liveEvents)
		m.liveEvents = append(m.liveEvents, ev)
		m.liveList.InsertItem(i, navItem{title: m.color.EventLine(ev), filter: m.plain.EventLine(ev), kind: kindEvent, idx: i})
	}
	if !m.livePaused && atBottom {
		m.liveList.Select(len(m.liveList.Items()) - 1)
	}
}

func (m *Model) handleLiveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.liveList.SettingFilter() {
		var cmd tea.Cmd
		m.liveList, cmd = m.liveList.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "q", "esc", "f":
		m.exitLive()
		return m, nil
	case " ", "p": // pause/resume — the only thing that toggles pause
		m.livePaused = !m.livePaused
		if !m.livePaused {
			m.liveList.Select(len(m.liveList.Items()) - 1)
		}
		return m, nil
	case "enter", "l", "right", "o":
		m.openLiveEvent() // drill in; never changes pause
		return m, nil
	case "G", "end":
		m.livePaused = false // jump to the newest and follow again
		m.liveList.Select(len(m.liveList.Items()) - 1)
		return m, nil
	}
	// Plain navigation: move the selection freely. It never pauses the feed.
	var cmd tea.Cmd
	m.liveList, cmd = m.liveList.Update(msg)
	return m, cmd
}

// openLiveEvent pauses on the selected event and drills into it. The live capture is
// re-read first so the navigator reflects everything streamed so far — otherwise a
// just-streamed tool isn't in the (stale) tree and the drill misses it. Esc returns
// to the (still buffering) feed.
func (m *Model) openLiveEvent() {
	it, ok := m.liveList.SelectedItem().(navItem)
	if !ok || it.idx >= len(m.liveEvents) {
		return
	}
	if m.liveCapt != nil {
		if err := m.openCapture(m.liveCapt); err != nil {
			return
		}
	}
	m.drillEvent(m.liveEvents[it.idx])
}

// drillEvent opens the exact item an event names: the specific tool call, the
// specific turn, else the agent's timeline. Assumes the relevant capture is loaded.
func (m *Model) drillEvent(e logview.Event) {
	if e.Kind == logview.EventStall {
		if idx := m.stallIndexForEvent(e); idx >= 0 {
			m.push()
			m.rawKind = streamDenPerf
			m.rawIdx = idx
			m.level = levelRawDetail
			m.clearSearch()
			m.refreshDetail()
			m.detail.GotoTop()
		}
		return
	}
	if e.SessionID == "" {
		return
	}
	agent, found := m.tree.Find(e.SessionID)
	if !found {
		return
	}
	m.push()
	m.agent = agent
	m.tools = agent.ToolEvents()
	m.clearSearch()

	if e.Kind == logview.EventTool {
		if ti := logview.ToolIndexByCallID(m.tools, e.CallID); ti >= 0 {
			m.toolView = true
			m.toolIdx = ti
			m.level = levelToolDetail
			m.detailFocusMsg = ""
			m.refreshDetail()
			m.detail.GotoTop()
			return
		}
	}
	// Locate the exact turn record. A single iteration can span several LLM records
	// (different surfaces), so prefer the record that actually holds the event's
	// message; fall back to the iteration when there's no message id.
	ti := agent.TurnIndexByMessageID(e.MsgID)
	if ti < 0 && e.Turn > 0 {
		ti = agent.TurnIndexByIteration(e.Turn)
	}
	if ti >= 0 {
		m.turnIdx = ti
		m.diffMode = false
		m.level = levelTurn
		m.detailFocusMsg = e.MsgID // refreshDetail scrolls to this exact message
		m.refreshDetail()
		return
	}
	m.level = levelAgent
	m.reload(true)
}

// stallIndexForEvent finds the Den perf row that produced a stall timeline event.
func (m *Model) stallIndexForEvent(e logview.Event) int {
	for i, r := range m.denPerf {
		if !r.IsStall() || !r.TS.Equal(e.Time) {
			continue
		}
		return i
	}
	return -1
}

func (m *Model) bodyHeight() int {
	h := m.height - 3 // breadcrumb + blank + footer
	if h < 1 {
		return 1
	}
	return h
}

func (m *Model) View() string {
	if !m.ready {
		return "loading…"
	}
	body := m.list.View()
	if m.level == levelLive {
		body = m.liveList.View()
	} else if m.isDetailLevel() {
		body = m.detail.View()
	}
	return m.breadcrumb() + "\n\n" + body + "\n" + m.footer()
}

func (m *Model) breadcrumb() string {
	if m.level == levelLive {
		where := "waiting for a session…"
		if m.liveDir != "" {
			where = logview.CaptureLabel(filepath.Base(m.liveDir))
		}
		state := followStyle.Render("● Live")
		if !m.liveFollowing() {
			state = pausedStyle.Render("⏸ Paused")
		}
		return state + crumbStyle.Render("  "+where)
	}
	if m.level == levelCaptures {
		return titleStyle.Render("Sessions") + crumbStyle.Render(fmt.Sprintf("  (%d captured)", len(m.captures)))
	}
	parts := []string{titleStyle.Render("Sessions"), m.sessionCrumb()}
	switch m.level {
	case levelSession:
		if m.sessionTimeline {
			parts = append(parts, crumbStyle.Render("timeline"))
		} else {
			parts = append(parts, crumbStyle.Render("agents"))
		}
	case levelAgent:
		c := m.agentCrumb()
		if m.toolView {
			c += crumbStyle.Render("  (tools)")
		}
		parts = append(parts, c)
	case levelTurn:
		parts = append(parts, m.agentCrumb(), crumbStyle.Render(fmt.Sprintf("turn %d", m.turnIdx+1)))
	case levelToolDetail:
		name := "tool"
		if m.toolIdx < len(m.tools) {
			name = m.tools[m.toolIdx].Name
		}
		parts = append(parts, m.agentCrumb(), crumbStyle.Render("→ "+name))
	case levelRawList:
		parts = append(parts, crumbStyle.Render(m.rawKind.title()))
	case levelRawDetail:
		parts = append(parts, crumbStyle.Render(m.rawKind.title()), crumbStyle.Render("detail"))
	case levelProblems:
		parts = append(parts, crumbStyle.Render("Problems"))
	case levelLog:
		parts = append(parts, crumbStyle.Render("Backend log"))
	case levelLogDetail:
		parts = append(parts, crumbStyle.Render("Backend log"), crumbStyle.Render("detail"))
	case levelCaptures, levelLive:
	}
	if m.level == levelTurn && m.diffMode {
		parts = append(parts, crumbStyle.Render("diff"))
	}
	return strings.Join(parts, crumbSep)
}

// sessionCrumb labels the open capture: the single session's headline, or the
// capture time and session count when it holds several.
func (m *Model) sessionCrumb() string {
	if roots := m.tree.Roots(); len(roots) > 1 {
		return crumbStyle.Render(fmt.Sprintf("%s · %d sessions", logview.CaptureLabel(m.captureName), len(roots)))
	}
	return crumbStyle.Render(clip(m.tree.Headline(), max(12, m.width/3)))
}

// agentCrumb is the agent name plus its outcome badge for a worker.
func (m *Model) agentCrumb() string {
	name := strings.TrimSpace(m.agent.AgentType)
	if name == "" {
		name = "—"
	}
	c := crumbStyle.Render(name)
	if !m.agent.IsCoordinator() {
		c += " " + m.color.StatusBadge(m.agent.Outcome.Status)
	}
	return c
}

func (m *Model) footer() string {
	if m.searching {
		hint := footerStyle.Render("   (Enter to jump, Esc to cancel)")
		return searchStyle.Render("/"+m.search) + footerStyle.Render("▏") + hint
	}
	if m.piping {
		hint := footerStyle.Render("   (Enter copy · or command · Esc cancel)")
		return searchStyle.Render("|"+m.pipeCmd) + footerStyle.Render("▏") + hint
	}
	if m.status != "" {
		return footerStyle.Render(m.status)
	}
	var actions string
	switch m.level {
	case levelCaptures:
		actions = "↑↓ move   Enter open session   / filter   f live   q quit"
	case levelSession:
		if m.sessionTimeline {
			actions = "↑↓ move   Enter drill in   t tree   / filter   f live   Esc back"
		} else {
			actions = "↑↓ move   Enter open   t timeline   / filter   f live   Esc back"
		}
	case levelAgent:
		open := "Enter view prompt"
		toggle := "t tools"
		if m.toolView {
			open, toggle = "Enter view tool", "t turns"
		}
		actions = "↑↓ move   " + open + "   " + toggle + "   / filter   Esc back"
	case levelTurn:
		diff := "d diff"
		if m.diffMode {
			diff = "d full"
		}
		actions = "←/→ prev/next turn   Space/b page   / find   | pipe   s system   " + diff
		if m.currentTurnHasChild() {
			actions += "   w worker"
		}
		actions += "   Esc back"
	case levelToolDetail:
		actions = "←/→ prev/next tool   Space/b page   g/G ends   / find   | pipe   Esc back"
	case levelProblems:
		actions = "↑↓ move   Enter go to problem   / filter   Esc back"
	case levelLog:
		show := "a all levels"
		if m.logShowAll {
			show = "a warn/err"
		}
		actions = "↑↓ move   Enter details   " + show + "   / filter   Esc back"
	case levelLogDetail:
		actions = "←/→ prev/next   Space/b page   / find   | pipe   Esc back"
	case levelRawList:
		actions = "↑↓ move   Enter details   / filter   Esc back"
	case levelRawDetail:
		actions = "←/→ prev/next   Space/b page   g/G ends   / find   | pipe   Esc back"
	case levelLive:
		if m.livePaused {
			actions = "↑↓ select   Enter drill in   Space/G resume   / filter   Esc stop"
		} else {
			actions = "↑↓ browse   Enter drill in   Space pause   / filter   f/Esc stop"
		}
	}
	left := footerStyle.Render(actions)
	if len(m.matches) > 0 {
		left += footerStyle.Render(fmt.Sprintf("   [%d/%d match · n/N]", m.matchPos+1, len(m.matches)))
	}
	if m.follow && m.level != levelLive {
		left = followStyle.Render("● following") + footerStyle.Render("   ") + left
	}
	return left
}
