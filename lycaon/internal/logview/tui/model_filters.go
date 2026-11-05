package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	logview "github.com/lycaon/lycaon/internal/logview"
)

func (m modelFilter) ensureCaptures() {
	if m.captures != nil {
		return
	}
	if caps, err := logview.ListCaptureSummaries(); err == nil {
		m.captures = caps
	}
}

// items builds the list rows for the current level.
func (m modelFilter) items() []list.Item {
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

func (m modelFilter) visibleLogs() []logview.LogRecord {
	if m.logShowAll {
		return m.logRecs
	}
	return logview.FilterLogs(m.logRecs, warnRank)
}

func (m modelFilter) rawItems() []list.Item {
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

func (m modelFilter) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	return m.Model, nil
}

func (m modelFilter) runSearch() {
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

func (m modelFilter) stepMatch(dir int) {
	if len(m.matches) == 0 {
		return
	}
	m.matchPos = (m.matchPos + dir + len(m.matches)) % len(m.matches)
	m.detail.SetYOffset(m.matches[m.matchPos])
}

func (m modelFilter) clearSearch() {
	m.searching = false
	m.search = ""
	m.matches = nil
	m.matchPos = 0
}

func (m modelFilter) rawCount() int {
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

func (m modelNav) push() {
	m.stack = append(m.stack, frame{
		level: m.level, agent: m.agent, rawKind: m.rawKind,
		turnIdx: m.turnIdx, rawIdx: m.rawIdx, toolIdx: m.toolIdx, cursor: m.list.Index(),
	})
}

func (m modelNav) pop() {
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
	m.filter().clearSearch()
	switch {
	case m.level == levelLive:
		// the live feed list persists across the drill; nothing to rebuild
	case m.isDetailLevel():
		m.render().refreshDetail()
	default:
		m.reload(false)
		m.list.Select(f.cursor)
	}
}

// reload rebuilds the list for the current level; resetCursor selects the top.
func (m modelNav) reload(resetCursor bool) {
	idx := m.list.Index()
	m.list.SetItems(m.filter().items())
	if resetCursor {
		m.list.Select(0)
	} else if idx < len(m.list.Items()) {
		m.list.Select(idx)
	}
}

// detailSibling steps to the previous/next item in the underlying list while
// staying zoomed into the detail pane — next tool, next turn, next request, etc.
// It keeps the parent list cursor in sync so backing out lands on the right row.
func (m modelNav) detailSibling(delta int) {
	var idx *int
	var count int
	switch m.level {
	case levelTurn:
		idx, count = &m.turnIdx, len(m.agent.Turns)
	case levelToolDetail:
		idx, count = &m.toolIdx, len(m.tools)
	case levelRawDetail:
		idx, count = &m.rawIdx, m.filter().rawCount()
	case levelLogDetail:
		idx, count = &m.logIdx, len(m.filter().visibleLogs())
	default:
		return
	}
	next := clampIdx(*idx+delta, count)
	if next == *idx {
		return
	}
	*idx = next
	m.list.Select(next)
	m.filter().clearSearch()
	m.render().refreshDetail()
	m.detail.GotoTop()
}
