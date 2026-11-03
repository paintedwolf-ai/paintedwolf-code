package tui

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	logview "github.com/lycaon/lycaon/internal/logview"
)

// renderDetail produces the current detail content with the given display (color
// for the pane, plain for building the searchable line index).
func (m modelRender) renderDetail(d logview.Display) string {
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
		recs := m.filter().visibleLogs()
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
func (m modelRender) refreshDetail() {
	w := m.detail.Width
	m.detail.SetContent(m.renderDetail(m.color.WithWidth(w)))
	m.detailLines = strings.Split(m.renderDetail(m.plain.WithWidth(w)), "\n")
	switch {
	case m.search != "":
		m.filter().runSearch()
	case m.detailFocusMsg != "" && m.level == levelTurn:
		m.detail.SetYOffset(max(0, m.turnFocusOffset-1))
		m.detailFocusMsg = "" // one-shot: don't re-jump on later re-renders
	}
}

func (m modelRender) bodyHeight() int {
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
	return m.render().breadcrumb() + "\n\n" + body + "\n" + m.render().footer()
}

func (m modelRender) breadcrumb() string {
	if m.level == levelLive {
		where := "waiting for a session…"
		if m.liveDir != "" {
			where = logview.CaptureLabel(filepath.Base(m.liveDir))
		}
		state := followStyle.Render("● Live")
		if !m.live().liveFollowing() {
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
func (m modelRender) sessionCrumb() string {
	if roots := m.tree.Roots(); len(roots) > 1 {
		return crumbStyle.Render(fmt.Sprintf("%s · %d sessions", logview.CaptureLabel(m.captureName), len(roots)))
	}
	return crumbStyle.Render(clip(m.tree.Headline(), max(12, m.width/3)))
}

// agentCrumb is the agent name plus its outcome badge for a worker.
func (m modelRender) agentCrumb() string {
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

func (m modelRender) footer() string {
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
		if m.keys().currentTurnHasChild() {
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
