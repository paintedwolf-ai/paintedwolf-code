package logview

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// PromptOptions controls how a single call's transcript is rendered.
type PromptOptions struct {
	HideSystem bool // omit system messages (often a large rendered prompt)
	HeadLines  int  // cap content lines per message (0 = full)
	ShowTools  bool // list the call's available tool names
}

// WriteRawCall emits the selected call as indented JSON for piping into jq or a diff.
func WriteRawCall(w io.Writer, r LLMRecord) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// ExportPromptPlain returns a paste-friendly transcript: role, message body, and
// tool calls — no box-drawing gutters, soft-wrap, or call-metadata chrome.
func ExportPromptPlain(d Display, r LLMRecord, opt PromptOptions) string {
	var b strings.Builder
	for _, msg := range r.Messages {
		if opt.HideSystem && msg.Role == "system" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(msg.Role)
		b.WriteByte('\n')
		if text := strings.TrimRight(d.text(msg.Content), "\n"); strings.TrimSpace(text) != "" {
			b.WriteString(text)
			b.WriteByte('\n')
		}
		for _, tc := range msg.ToolCalls {
			b.WriteString("→ ")
			b.WriteString(orDash(tc.Name))
			b.WriteByte('\n')
			if pretty := prettyJSON(tc.Args); pretty != "" && strings.Contains(pretty, "\n") {
				b.WriteString(strings.TrimRight(pretty, "\n"))
				b.WriteByte('\n')
			} else if inline := inlineArgs(tc.Args); inline != "" {
				b.WriteString(inline)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

// SelectCall resolves a 1-based index into recs. index <= 0 selects the last call.
func SelectCall(recs []LLMRecord, index int) (LLMRecord, int, error) {
	if len(recs) == 0 {
		return LLMRecord{}, 0, fmt.Errorf("no LLM calls captured")
	}
	if index <= 0 {
		return recs[len(recs)-1], len(recs), nil
	}
	if index > len(recs) {
		return LLMRecord{}, 0, fmt.Errorf("call #%d out of range (have %d)", index, len(recs))
	}
	return recs[index-1], index, nil
}

// RenderPrompt prints a single call's full transcript: a header of call metadata
// followed by each message as a labeled block with tool calls and tool results.
func (d Display) RenderPrompt(w io.Writer, r LLMRecord, index int, opt PromptOptions) error {
	lines, _ := d.promptBlocks(r, index, opt)
	fmt.Fprintln(w, strings.Join(lines, "\n"))
	return nil
}

// RenderPromptFocus renders the transcript and returns the line offset of the message
// with focusID (the line of its block header), so the detail pane can scroll straight
// to the message a timeline event named. Returns 0 when focusID is empty or absent.
func (d Display) RenderPromptFocus(w io.Writer, r LLMRecord, index int, opt PromptOptions, focusID string) int {
	lines, offsets := d.promptBlocks(r, index, opt)
	fmt.Fprintln(w, strings.Join(lines, "\n"))
	return offsets[focusID]
}

// promptBlocks renders the transcript into lines and records, per message ID, the
// line where its block starts.
func (d Display) promptBlocks(r LLMRecord, index int, opt PromptOptions) (lines []string, offsets map[string]int) {
	split := func(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

	var hb strings.Builder
	d.renderPromptHeader(&hb, r, index)
	lines = split(hb.String())
	if opt.ShowTools && len(r.ToolNames) > 0 {
		lines = append(lines, "", d.Bold("Available tools")+d.Dim(fmt.Sprintf(" (%d)", len(r.ToolNames))))
		lines = append(lines, split(d.indentWrap(strings.Join(r.ToolNames, ", "), "  ", 0))...)
	}

	offsets = map[string]int{}
	for _, m := range r.Messages {
		if opt.HideSystem && m.Role == "system" {
			continue
		}
		lines = append(lines, "")
		if m.ID != "" {
			offsets[m.ID] = len(lines)
		}
		var mb strings.Builder
		d.RenderMessage(&mb, m, opt)
		lines = append(lines, split(mb.String())...)
	}
	return lines, offsets
}

func (d Display) renderPromptHeader(w io.Writer, r LLMRecord, index int) {
	ruleW := 60
	if d.Width > 0 && d.Width < ruleW {
		ruleW = d.Width
	}
	rule := strings.Repeat("━", ruleW)
	fmt.Fprintln(w, d.Dim(rule))
	fmt.Fprintf(w, "%s  %s  %s\n",
		d.Bold(fmt.Sprintf("LLM call #%d", index)),
		d.Cyan(orDash(r.Surface)),
		d.BoldCyan(orDash(r.AgentType)))
	fmt.Fprintln(w, d.Dim(rule))

	tokens := "—"
	if r.Usage != nil {
		tokens = fmt.Sprintf("↑%s prompt  ↓%s completion", human(r.Usage.PromptTokens), human(r.Usage.CompletionTokens))
	}
	fmt.Fprintf(w, "  %s %s\n", d.Dim("time     "), d.time(r.TS))
	fmt.Fprintf(w, "  %s %s\n", d.Dim("model    "), orDash(r.Model))
	fmt.Fprintf(w, "  %s %s\n", d.Dim("iteration"), fmt.Sprintf("%d/%d", r.Iteration, r.MaxIterations))
	fmt.Fprintf(w, "  %s %s    %s %s\n", d.Dim("tokens   "), tokens, d.Dim("took"), dur(r.DurationMS))
	fmt.Fprintf(w, "  %s %s    %s %d available\n", d.Dim("session  "), shortID(r.SessionID), d.Dim("tools"), len(r.ToolNames))
}

// RenderMessage prints one labeled transcript block; reused by the TUI detail pane.
func (d Display) RenderMessage(w io.Writer, m LLMMessage, opt PromptOptions) {
	tsPart, tsVisible := "", 0
	if !m.TS.IsZero() {
		ts := d.time(m.TS)
		tsPart = " " + d.Dim(ts)
		tsVisible = 1 + len([]rune(ts))
	}
	// "┌─ " (3) + role + " " (1) + ts segment
	used := 4 + len(m.Role) + tsVisible
	dashCount := max(2, 56-len(m.Role)-tsVisible)
	if d.Width > 0 {
		dashCount = max(2, d.Width-used)
	}
	bar := fmt.Sprintf("┌─ %s%s ", d.Role(m.Role), tsPart)
	fmt.Fprintln(w, bar+d.Dim(strings.Repeat("─", dashCount)))

	text := d.text(m.Content)
	hasText := strings.TrimSpace(text) != ""
	if hasText {
		fmt.Fprintln(w, d.indentWrap(d.highlightContent(m.Role, text), "│ ", opt.HeadLines))
	}
	for _, tc := range m.ToolCalls {
		d.renderToolCall(w, tc)
	}
	if !hasText && len(m.ToolCalls) == 0 {
		fmt.Fprintln(w, d.Dim("│ (no text)"))
	}
}

func (d Display) renderToolCall(w io.Writer, tc LLMToolCall) {
	head := d.Dim("│ ") + d.Yellow("→ "+orDash(tc.Name))
	if inline := inlineArgs(tc.Args); inline != "" {
		head += " " + d.Dim(inline)
	}
	fmt.Fprintln(w, head)
	if pretty := prettyJSON(tc.Args); pretty != "" && strings.Contains(pretty, "\n") {
		fmt.Fprintln(w, d.indentWrap(d.hlJSON(pretty), "│   ", 0))
	}
}

// highlightContent syntax-highlights message text by role: tool results as JSON
// (when they look like JSON), everything else as markdown.
func (d Display) highlightContent(role, text string) string {
	if role == "tool" {
		return d.hlAuto(text)
	}
	return d.highlight(text, "markdown")
}

// inlineArgs renders tool args inline when small, deferring large payloads to the
// indented pretty-printed block.
func inlineArgs(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return ""
	}
	if !strings.Contains(prettyJSON(raw), "\n") {
		return string(raw)
	}
	return ""
}
