package tui

import (
	"sort"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	logview "github.com/lycaon/lycaon/internal/logview"
)

// toggleLive enters or leaves the live event feed.
func (m modelLive) toggleLive() tea.Cmd {
	if m.level == levelLive {
		m.exitLive()
		return nil
	}
	m.enterLive()
	return tick()
}

// enterLive opens the full-screen live feed: a selectable list of streamed events.
// Space pauses/resumes; Enter drills in (independent of pause); scrolling up pauses.
func (m modelLive) enterLive() {
	m.nav().push()
	m.level = levelLive
	m.follow = true
	m.livePaused = false
	m.liveEvents = nil
	m.liveList = list.New(nil, singleLineDelegate{}, m.width, m.render().bodyHeight())
	m.liveList.SetShowTitle(false)
	m.liveList.SetShowHelp(false)
	m.liveList.SetShowStatusBar(false)
	m.liveList.DisableQuitKeybindings()
	m.liveDir = ""
	m.attachLive()
}

func (m modelLive) exitLive() {
	m.follow = false
	m.nav().pop()
}

// attachLive (re)points the feed at the newest capture, starting its tailers at the
// end so only new events stream — like tail -f.
func (m modelLive) attachLive() {
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
func (m modelLive) liveFollowing() bool { return !m.livePaused }

// poll appends newly-captured events to the feed, re-attaching if the sidecar
// rotated, and keeps the selection pinned to the newest event unless paused — so you
// can drill in and return to a still-live feed.
func (m modelLive) poll() {
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

func (m modelLive) handleLiveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.liveList.SettingFilter() {
		var cmd tea.Cmd
		m.liveList, cmd = m.liveList.Update(msg)
		return m.Model, cmd
	}
	switch msg.String() {
	case "q", "esc", "f":
		m.exitLive()
		return m.Model, nil
	case " ", "p": // pause/resume — the only thing that toggles pause
		m.livePaused = !m.livePaused
		if !m.livePaused {
			m.liveList.Select(len(m.liveList.Items()) - 1)
		}
		return m.Model, nil
	case "enter", "l", "right", "o":
		m.openLiveEvent() // drill in; never changes pause
		return m.Model, nil
	case "G", "end":
		m.livePaused = false // jump to the newest and follow again
		m.liveList.Select(len(m.liveList.Items()) - 1)
		return m.Model, nil
	}
	// Plain navigation: move the selection freely. It never pauses the feed.
	var cmd tea.Cmd
	m.liveList, cmd = m.liveList.Update(msg)
	return m.Model, cmd
}

// openLiveEvent pauses on the selected event and drills into it. The live capture is
// re-read first so the navigator reflects everything streamed so far — otherwise a
// just-streamed tool isn't in the (stale) tree and the drill misses it. Esc returns
// to the (still buffering) feed.
func (m modelLive) openLiveEvent() {
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
func (m modelLive) drillEvent(e logview.Event) {
	if e.Kind == logview.EventStall {
		if idx := m.stallIndexForEvent(e); idx >= 0 {
			m.nav().push()
			m.rawKind = streamDenPerf
			m.rawIdx = idx
			m.level = levelRawDetail
			m.filter().clearSearch()
			m.render().refreshDetail()
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
	m.nav().push()
	m.agent = agent
	m.tools = agent.ToolEvents()
	m.filter().clearSearch()

	if e.Kind == logview.EventTool {
		if ti := logview.ToolIndexByCallID(m.tools, e.CallID); ti >= 0 {
			m.toolView = true
			m.toolIdx = ti
			m.level = levelToolDetail
			m.detailFocusMsg = ""
			m.render().refreshDetail()
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
		m.render().refreshDetail()
		return
	}
	m.level = levelAgent
	m.nav().reload(true)
}

// stallIndexForEvent finds the Den perf row that produced a stall timeline event.
func (m modelLive) stallIndexForEvent(e logview.Event) int {
	for i, r := range m.denPerf {
		if !r.IsStall() || !r.TS.Equal(e.Time) {
			continue
		}
		return i
	}
	return -1
}

// openProblem jumps from the problems list to the exact place a problem occurred.
func (m modelNav) openProblem(p logview.Problem) {
	if p.Kind == logview.ProblemHTTP {
		m.push()
		m.rawKind = streamHTTP
		m.rawIdx = p.HTTPIndex
		m.level = levelRawDetail
		m.filter().clearSearch()
		m.render().refreshDetail()
		m.detail.GotoTop()
		return
	}
	if p.Kind == logview.ProblemStall {
		m.push()
		m.rawKind = streamDenPerf
		m.rawIdx = p.StallIndex
		m.level = levelRawDetail
		m.filter().clearSearch()
		m.render().refreshDetail()
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
	m.filter().clearSearch()
	switch {
	case p.ToolIndex >= 0:
		m.toolView = true
		m.toolIdx = p.ToolIndex
		m.level = levelToolDetail
		m.render().refreshDetail()
		m.detail.GotoTop()
	case p.TurnIndex >= 0:
		m.turnIdx = p.TurnIndex
		m.level = levelTurn
		m.diffMode = false
		m.render().refreshDetail()
		m.detail.GotoTop()
	default: // worker outcome → its timeline
		m.level = levelAgent
		m.reload(true)
	}
}
