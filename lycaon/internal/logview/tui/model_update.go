package tui

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	logview "github.com/lycaon/lycaon/internal/logview"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.SetSize(msg.Width, m.render().bodyHeight())
		if m.liveList.Items() != nil || m.level == levelLive {
			m.liveList.SetSize(msg.Width, m.render().bodyHeight())
		}
		if !m.ready {
			m.detail = viewport.New(msg.Width, m.render().bodyHeight())
			// Content soft-wraps to width, so disable horizontal scroll and let
			// h/← mean "back" instead.
			m.detail.KeyMap.Left.SetEnabled(false)
			m.detail.KeyMap.Right.SetEnabled(false)
			m.ready = true
			m.nav().reload(false)
			m.list.Select(0)
			if m.cfg.Follow {
				m.live().enterLive()
			}
		} else {
			m.detail.Width = msg.Width
			m.detail.Height = m.render().bodyHeight()
			if m.isDetailLevel() {
				m.render().refreshDetail()
			}
		}
		return m, nil

	case tickMsg:
		if m.follow {
			m.live().poll()
			return m, tick()
		}
		return m, nil

	case clearStatusMsg:
		if msg.gen == m.statusGen {
			m.status = ""
		}
		return m, nil

	case tea.KeyMsg:
		return m.keys().handleKey(msg)
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

func (m modelKeyHandler) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.level == levelLive {
		return m.live().handleLiveKey(msg)
	}
	if m.isDetailLevel() {
		return m.handleDetailKey(msg)
	}
	if m.list.SettingFilter() {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m.Model, cmd
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m.Model, tea.Quit
	case "esc", "backspace", "left", "h":
		m.nav().pop()
		return m.Model, nil
	case "enter", "right", "l", "o":
		m.nav().descend()
		return m.Model, nil
	case "f":
		// Sequenced, not inlined: m is a pointer, but keeping the mutation
		// ahead of the return states the ordering outright.
		cmd := m.live().toggleLive()
		return m.Model, cmd
	case "t":
		if m.level == levelAgent {
			m.toolView = !m.toolView
			m.nav().reload(true)
			return m.Model, nil
		}
		if m.level == levelSession {
			m.sessionTimeline = !m.sessionTimeline
			m.nav().reload(true)
			return m.Model, nil
		}
	case "a":
		if m.level == levelLog {
			m.logShowAll = !m.logShowAll
			m.nav().reload(true)
			return m.Model, nil
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m.Model, cmd
}

func (m modelKeyHandler) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.searching {
		return m.filter().handleSearchKey(msg)
	}
	if m.piping {
		return m.pipe().handlePipeKey(msg)
	}
	switch msg.String() {
	case "q", "esc", "backspace":
		m.nav().pop()
		return m.Model, nil
	case "right", "l":
		m.nav().detailSibling(1)
		return m.Model, nil
	case "left", "h":
		m.nav().detailSibling(-1)
		return m.Model, nil
	case "g", "home":
		m.detail.GotoTop()
		return m.Model, nil
	case "G", "end":
		m.detail.GotoBottom()
		return m.Model, nil
	case "/":
		m.searching = true
		m.search = ""
		m.status = ""
		return m.Model, nil
	case "|":
		m.pipe().startPipe()
		return m.Model, nil
	case "n":
		m.filter().stepMatch(1)
		return m.Model, nil
	case "N":
		m.filter().stepMatch(-1)
		return m.Model, nil
	case "s":
		if m.level == levelTurn {
			m.showSystem = !m.showSystem
			m.render().refreshDetail()
		}
		return m.Model, nil
	case "d":
		if m.level == levelTurn {
			m.diffMode = !m.diffMode
			m.render().refreshDetail()
			m.detail.GotoTop()
		}
		return m.Model, nil
	case "w":
		if m.level == levelTurn {
			m.jumpToChild()
		}
		return m.Model, nil
	}
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg) // space/b page, d/u half, j/k line
	return m.Model, cmd
}

// jumpToChild navigates from a coordinator turn's task() call into the worker
// session it spawned, pushing a frame so Esc returns to the turn.
func (m modelKeyHandler) jumpToChild() {
	for _, id := range logview.ChildSessionsIn(m.agent.Turns[m.turnIdx]) {
		if child, ok := m.tree.Find(id); ok && child != m.agent {
			m.nav().push()
			m.filter().clearSearch()
			m.agent = child
			m.level = levelAgent
			m.nav().reload(true)
			return
		}
	}
}

// currentTurnHasChild reports whether the open turn spawned a reachable worker.
func (m modelKeyHandler) currentTurnHasChild() bool {
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
func (m modelNav) descend() {
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
			m.live().drillEvent(m.sessionEvents[it.idx])
		}
	case kindLogRow:
		m.push()
		m.logIdx = it.idx
		m.level = levelLogDetail
		m.filter().clearSearch()
		m.render().refreshDetail()
		m.detail.GotoTop()
	case kindTurn:
		m.push()
		m.turnIdx = it.idx
		m.level = levelTurn
		m.diffMode = false
		m.filter().clearSearch()
		m.render().refreshDetail()
		m.detail.GotoTop()
	case kindTool:
		m.push()
		m.toolIdx = it.idx
		m.level = levelToolDetail
		m.filter().clearSearch()
		m.render().refreshDetail()
		m.detail.GotoTop()
	case kindHTTPRow, kindSSERow, kindDenPerfRow:
		m.push()
		m.rawIdx = it.idx
		m.level = levelRawDetail
		m.filter().clearSearch()
		m.render().refreshDetail()
		m.detail.GotoTop()
	}
}
