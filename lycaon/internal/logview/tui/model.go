// Package tui is the interactive session browser over a debug capture. It reuses
// internal/logview for all data access and rendering, so the TUI and the plain CLI
// present identical content. The browser is a drill-down navigator: Sessions (all
// captures) → one session's agents → turns → prompt, with the raw HTTP/SSE streams
// as a secondary view.
package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	logview "github.com/lycaon/lycaon/internal/logview"
	"github.com/lycaon/lycaon/internal/runeclamp"
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
	capt            *logview.Capture
	cfg             logview.Config
	color           logview.Display
	plain           logview.Display
	level           level
	showSystem      bool
	sessionTimeline bool
	captures        []logview.CaptureSummary
	captureName     string
	tree            *logview.SessionTree
	modelRecords
}

type modelRecords struct {
	httpRecs      []logview.HTTPRecord
	sse           []logview.SSERecord
	denPerf       []logview.DenPerfRecord
	llm           []logview.LLMRecord
	sessions      []logview.SessionRecord
	problems      []logview.Problem
	logRecs       []logview.LogRecord
	logIdx        int
	logShowAll    bool
	sessionEvents []logview.Event
	modelSelection
}

type modelSelection struct {
	agent    *logview.Agent
	turnIdx  int
	rawKind  streamKind
	rawIdx   int
	toolView bool
	tools    []logview.ToolEvent
	toolIdx  int
	diffMode bool
	list     list.Model
	detail   viewport.Model
	modelSearch
}

type modelSearch struct {
	detailLines     []string
	detailFocusMsg  string
	turnFocusOffset int
	stack           []frame
	searching       bool
	search          string
	matches         []int
	matchPos        int
	piping          bool
	pipeCmd         string
	modelWindow
}

type modelWindow struct {
	status        string
	statusGen     int
	follow        bool
	width, height int
	ready         bool
	liveList      list.Model
	liveEvents    []logview.Event
	livePaused    bool
	liveCapt      *logview.Capture
	modelLiveTail
}

type modelLiveTail struct {
	liveEngine   *logview.FollowEngine
	liveTailLLM  *logview.Tailer[logview.LLMRecord]
	liveTailSess *logview.Tailer[logview.SessionRecord]
	liveDir      string
}

type (
	modelFilter     struct{ *Model }
	modelKeyHandler struct{ *Model }
	modelNav        struct{ *Model }
	modelRender     struct{ *Model }
	modelLive       struct{ *Model }
	modelPipe       struct{ *Model }
)

func (m *Model) filter() modelFilter   { return modelFilter{m} }
func (m *Model) keys() modelKeyHandler { return modelKeyHandler{m} }
func (m *Model) nav() modelNav         { return modelNav{m} }
func (m *Model) render() modelRender   { return modelRender{m} }
func (m *Model) live() modelLive       { return modelLive{m} }
func (m *Model) pipe() modelPipe       { return modelPipe{m} }
func (m *Model) livePoll()             { m.live().poll() }
func (m *Model) visibleLogs() []logview.LogRecord {
	return m.filter().visibleLogs()
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
		sessionTimeline: true,
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

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) Init() tea.Cmd {
	if m.cfg.Follow {
		return tick()
	}
	return nil
}
