package logview

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// ToolEvent is one tool the agent invoked, paired with its result — the unit of the
// tool view, which swaps an agent's turns for the flat sequence of its tool calls.
type ToolEvent struct {
	Name      string          `json:"name"`
	CallID    string          `json:"call_id,omitempty"` // tool_call id, to locate this exact call
	Args      json.RawMessage `json:"args,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"` // assistant text before the call
	Result    string          `json:"result,omitempty"`    // the tool result content
	TS        time.Time       `json:"ts"`
	Batch     int             `json:"batch,omitempty"`
	BatchSize int             `json:"batch_size,omitempty"`
}

// ToolIndexByCallID returns the position of a tool call in events, or -1.
func ToolIndexByCallID(events []ToolEvent, callID string) int {
	if callID == "" {
		return -1
	}
	for i := range events {
		if events[i].CallID == callID {
			return i
		}
	}
	return -1
}

// ToolEvents flattens all of an agent's transcripts into the ordered tool calls it
// made, each paired with its result. It walks every turn (oldest first), pairing
// calls with the results that follow them, and dedupes by tool_call id — so the full
// tool history is captured even when a long agent's later transcripts compact early
// calls out.
func (a *Agent) ToolEvents() []ToolEvent {
	seen := map[string]bool{}
	var out []ToolEvent
	batch := 0
	for _, turn := range a.Turns {
		var calls []ToolEvent
		var results []string
		var resultTS []time.Time
		for _, m := range turn.Messages {
			switch m.Role {
			case "assistant":
				reasoning := messageText(m.Content)
				if len(m.ToolCalls) > 0 {
					batch++
				}
				for _, tc := range m.ToolCalls {
					calls = append(calls, ToolEvent{
						Name: tc.Name, CallID: tc.ID, Args: tc.Args, Reasoning: reasoning, TS: m.TS,
						Batch: batch, BatchSize: len(m.ToolCalls),
					})
				}
			case "tool":
				results = append(results, messageText(m.Content))
				resultTS = append(resultTS, m.TS)
			}
		}
		for i := range calls {
			if i >= len(results) {
				break // unanswered tail — its result is paired in a later turn
			}
			calls[i].Result = results[i]
			if calls[i].TS.IsZero() {
				calls[i].TS = resultTS[i]
			}
			if id := calls[i].CallID; id != "" {
				if seen[id] {
					continue // already captured from an earlier turn
				}
				seen[id] = true
			}
			out = append(out, calls[i])
		}
	}
	return out
}

// RenderTools prints an agent's tool calls as plain text (the CLI tools view).
func (d Display) RenderTools(w io.Writer, a *Agent, events []ToolEvent) error {
	fmt.Fprintf(w, "%s %s  %s\n", d.BoldCyan(orDash(a.AgentType)), d.Dim(shortID(a.SessionID)),
		d.Dim(fmt.Sprintf("%d tools", len(events))))
	for i, e := range events {
		fmt.Fprintln(w, d.ToolRow(i+1, e))
	}
	return nil
}

// ToolRow renders one tool event as a compact list line (name, args, result).
func (d Display) ToolRow(n int, e ToolEvent) string {
	row := fmt.Sprintf("%s %s %s",
		d.Dim(fmt.Sprintf("%-3d", n)),
		d.Dim(d.time(e.TS)),
		d.Yellow("→ "+orDash(e.Name)))
	if args := inlineJSON(e.Args); args != "" {
		row += "  " + d.Dim(truncate(args, 36))
	}
	if res := oneLine(d.plainText(e.Result)); res != "" {
		row += "  " + d.Dim("← "+truncate(res, 44))
	}
	return row
}

// ToolDetail renders the full inspection of one tool event for the detail pane.
func (d Display) ToolDetail(w io.Writer, e ToolEvent) {
	fmt.Fprintf(w, "%s  %s\n", d.Yellow("→ "+orDash(e.Name)), d.Dim(d.time(e.TS)))
	if r := d.plainText(e.Reasoning); r != "" {
		fmt.Fprintf(w, "\n%s\n%s\n", d.Dim("reasoning"), d.indentWrap(d.highlight(r, "markdown"), "  ", 0))
	}
	if pretty := prettyJSON(e.Args); pretty != "" {
		fmt.Fprintf(w, "\n%s\n%s\n", d.Dim("args"), d.indentWrap(d.hlJSON(pretty), "  ", 0))
	}
	if res := d.plainText(e.Result); res != "" {
		fmt.Fprintf(w, "\n%s\n%s\n", d.Dim("result"), d.indentWrap(d.hlAuto(res), "  ", 0))
	}
}

// inlineJSON renders compact JSON args as a single line, or empty for trivial ones.
func inlineJSON(raw json.RawMessage) string {
	s := string(raw)
	if s == "" || s == "{}" || s == "null" {
		return ""
	}
	return oneLine(s)
}
