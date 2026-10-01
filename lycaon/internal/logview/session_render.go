package logview

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"io"
	"strings"
)

// SessionTree loads the capture and aggregates it into the agent topology.
func (c *Capture) SessionTree() (*SessionTree, error) {
	sessions, err := c.Sessions()
	if err != nil {
		return nil, err
	}
	llm, err := c.LLM()
	if err != nil {
		return nil, err
	}
	return BuildSessionTree(sessions, llm), nil
}

// StatusBadge renders a colored glyph+word for an outcome, or empty for a driver.
func (d Display) StatusBadge(s OutcomeStatus) string {
	switch s {
	case StatusComplete:
		return d.Green("✓ complete")
	case StatusPartial:
		return d.Yellow("⚠ partial")
	case StatusBlocked:
		return d.Yellow("⊘ blocked")
	case StatusFailed:
		return d.Red("✗ failed")
	case StatusRunning:
		return d.Cyan("⋯ running")
	default:
		return d.Dim("· unknown")
	}
}

// AgentRow renders one collapsed agent line for the session tree.
func (d Display) AgentRow(a *Agent) string {
	indent := strings.Repeat("  ", a.Depth)
	glyph := ""
	if a.Depth > 0 {
		glyph = d.Dim("└ ")
	}
	name := d.BoldCyan(orDash(a.AgentType))

	var meta []string
	if !a.IsCoordinator() {
		meta = append(meta, d.StatusBadge(a.Outcome.Status))
	}
	meta = append(meta, d.Dim(fmt.Sprintf("%d turns", len(a.Turns))))
	if tok := a.PromptTokens(); tok > 0 {
		meta = append(meta, d.Dim(human(tok)+" tok"))
	}
	if desc := d.agentDescriptor(a); desc != "" {
		meta = append(meta, d.Dim(truncate(desc, 64)))
	}
	return indent + glyph + name + "  " + strings.Join(meta, d.Dim(" · "))
}

// agentDescriptor shows the coordinator surface or worker task focus.
func (d Display) agentDescriptor(a *Agent) string {
	if a.IsCoordinator() {
		// Show the user request so multiple sessions in one capture are distinct.
		if task := cleanTask(a.Task); task != "" {
			return task
		}
		return a.Surface
	}
	if s := scopeDescriptor(a.Task); s != "" {
		return s
	}
	return a.Surface
}

// TurnRow renders one turn line in an agent timeline: what prompted it and any
// flag (a rejection or a forced closeout).
func (d Display) TurnRow(n int, r LLMRecord) string {
	tok := ""
	if r.Usage != nil {
		tok = fmt.Sprintf("↑%d", r.Usage.PromptTokens)
	}
	trigger, flag := turnTrigger(r)
	row := fmt.Sprintf("%s %s  %s  %s",
		d.Dim(fmt.Sprintf("turn %-3d", n)),
		d.Dim(d.time(r.TS)),
		d.Cyan(orDash(r.Surface)),
		d.Dim(truncate(oneLine(trigger), 64)),
	)
	if tok != "" {
		row += "  " + d.Dim(tok)
	}
	switch flag {
	case "reject":
		row += "  " + d.Red("⚠ rejected")
	case "closeout":
		row += "  " + d.Yellow("⚠ forced final turn")
	}
	return row
}

// turnTrigger summarizes the last message that prompted a call and classifies it.
func turnTrigger(r LLMRecord) (text, flag string) {
	if len(r.Messages) == 0 {
		return "", ""
	}
	last := r.Messages[len(r.Messages)-1]
	text = messageText(last.Content)
	switch {
	case strings.Contains(text, "[host:worker-closeout]"):
		return "forced final turn", "closeout"
	case strings.HasPrefix(strings.TrimSpace(text), hostmarker.Rejected):
		return text, "reject"
	case strings.Contains(text, "[host:loop-wake]"):
		return "host wake", ""
	}
	if last.Role == "tool" {
		return "← tool result: " + text, ""
	}
	return text, ""
}

// RenderSessionSummary prints the session narrative as plain text (the CLI L0 view).
func (d Display) RenderSessionSummary(w io.Writer, tree *SessionTree) error {
	if tree == nil || tree.Root == nil {
		fmt.Fprintln(w, d.Dim("no session captured — run ./task den:sidecar:full-debug first"))
		return nil
	}
	calls, tokens := 0, 0
	for _, a := range tree.Agents {
		calls += len(a.Turns)
		tokens += a.PromptTokens()
	}
	fmt.Fprintln(w, d.Bold("Session")+"  "+d.plainText(tree.Headline()))
	fmt.Fprintf(w, "%s %d agents · %d calls · %s prompt tokens\n",
		d.Dim("       "), len(tree.Agents), calls, human(tokens))
	fmt.Fprintln(w)
	for _, a := range tree.Agents {
		fmt.Fprintln(w, d.AgentRow(a))
	}
	return nil
}
