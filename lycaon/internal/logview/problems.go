package logview

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ProblemKind classifies an entry in the session-wide problems list.
type ProblemKind int

const (
	ProblemWorker    ProblemKind = iota // a worker leg that did not complete cleanly
	ProblemRejection                    // a tool/host rejection on a turn
	ProblemToolError                    // a command that exited non-zero
	ProblemHTTP                         // a 4xx/5xx response
	ProblemStall                        // a Den main-thread freeze (den-perf.jsonl)
)

// Problem is one thing that went wrong, with enough location to navigate to it.
type Problem struct {
	Kind       ProblemKind
	Severity   OutcomeStatus // drives the row color/glyph
	Summary    string
	AgentID    string    // session id for agent-anchored problems
	TurnIndex  int       // index into the agent's turns, or -1
	ToolIndex  int       // index into the agent's tool events, or -1
	HTTPIndex  int       // index into the HTTP records, or -1
	StallIndex int       // index into the DenPerf records, or -1
	TS         time.Time // when it happened, for ordering
}

// CollectProblems scans a session tree, HTTP records, and Den stall lines for
// everything worth flagging — newest signals last (chronological).
func CollectProblems(tree *SessionTree, httpRecs []HTTPRecord, denPerf []DenPerfRecord) []Problem {
	var out []Problem
	if tree != nil {
		for _, a := range tree.Agents {
			out = append(out, agentProblems(a)...)
		}
	}
	for i, r := range httpRecs {
		if r.Status < 400 || isBenignHTTPError(r) {
			continue
		}
		sev := StatusBlocked
		if r.Status >= 500 {
			sev = StatusFailed
		}
		out = append(out, Problem{
			Kind: ProblemHTTP, Severity: sev,
			Summary:   fmt.Sprintf("%d %s %s", r.Status, r.Method, r.Path),
			TurnIndex: -1, ToolIndex: -1, HTTPIndex: i, StallIndex: -1, TS: r.TS,
		})
	}
	for i, r := range denPerf {
		if !r.IsStall() {
			continue
		}
		ms := r.StallMS()
		sev := StatusBlocked
		if ms >= 1000 {
			sev = StatusFailed
		}
		summary := r.Event
		if ms > 0 {
			summary = fmt.Sprintf("%s %dms", r.Event, ms)
		}
		if recent := r.Recent(); recent != "" {
			summary = fmt.Sprintf("%s · %s", summary, truncate(recent, 60))
		}
		out = append(out, Problem{
			Kind: ProblemStall, Severity: sev,
			Summary:   summary,
			TurnIndex: -1, ToolIndex: -1, HTTPIndex: -1, StallIndex: i, TS: r.TS,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out
}

func agentProblems(a *Agent) []Problem {
	var out []Problem
	if !a.IsCoordinator() && isBadOutcome(a.Outcome.Status) {
		ts := time.Time{}
		if n := len(a.Turns); n > 0 {
			ts = a.Turns[n-1].TS
		}
		out = append(out, Problem{
			Kind: ProblemWorker, Severity: a.Outcome.Status,
			Summary:   fmt.Sprintf("%s — %s", orDash(a.AgentType), orWord(a.Outcome.Detail, string(a.Outcome.Status))),
			AgentID:   a.SessionID,
			TurnIndex: -1, ToolIndex: -1, HTTPIndex: -1, StallIndex: -1, TS: ts,
		})
	}
	for i, turn := range a.Turns {
		if _, flag := turnTrigger(turn); flag == "reject" {
			out = append(out, Problem{
				Kind: ProblemRejection, Severity: StatusBlocked,
				Summary:   fmt.Sprintf("%s turn %d — %s", orDash(a.AgentType), i+1, rejectSummary(turn)),
				AgentID:   a.SessionID,
				TurnIndex: i, ToolIndex: -1, HTTPIndex: -1, StallIndex: -1, TS: turn.TS,
			})
		}
	}
	for j, ev := range a.ToolEvents() {
		if code, bad := toolExitFailure(ev.Result); bad {
			out = append(out, Problem{
				Kind: ProblemToolError, Severity: StatusFailed,
				Summary:   fmt.Sprintf("%s → %s — exit %s", orDash(a.AgentType), orDash(ev.Name), code),
				AgentID:   a.SessionID,
				TurnIndex: -1, ToolIndex: j, HTTPIndex: -1, StallIndex: -1, TS: ev.TS,
			})
		}
	}
	return out
}

func isBadOutcome(s OutcomeStatus) bool {
	return s == StatusFailed || s == StatusPartial || s == StatusBlocked
}

// isBenignHTTPError filters out expected 4xx noise: the frontend polls a
// placeholder session id before the session exists, so those 404s are a bootstrap
// race, not a failure worth flagging.
func isBenignHTTPError(r HTTPRecord) bool {
	return strings.Contains(r.Path, "__session_create_pending__")
}

// toolExitFailure reports a non-zero command exit (the clean, high-signal tool
// error; benign signals like git "available":false are ignored).
func toolExitFailure(result string) (string, bool) {
	const key = `"ExitCode":`
	idx := strings.Index(result, key)
	if idx < 0 {
		return "", false
	}
	rest := result[idx+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	code := rest[:end]
	if code == "" || code == "0" {
		return "", false
	}
	return code, true
}

// rejectSummary pulls the structured Code (or first line) out of a rejection turn.
func rejectSummary(turn LLMRecord) string {
	text, _ := turnTrigger(turn)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Code:") {
			return strings.TrimSpace(line)
		}
	}
	return truncate(oneLine(text), 70)
}

// ProblemRow renders one problem as a list line.
func (d Display) ProblemRow(p Problem) string {
	glyph := d.Yellow("⚠")
	if p.Severity == StatusFailed {
		glyph = d.Red("✗")
	}
	return fmt.Sprintf("%s %s %s", glyph, d.Dim(problemKindLabel(p.Kind)), d.plainText(p.Summary))
}

func problemKindLabel(k ProblemKind) string {
	switch k {
	case ProblemWorker:
		return "worker "
	case ProblemRejection:
		return "reject "
	case ProblemToolError:
		return "tool   "
	case ProblemHTTP:
		return "http   "
	case ProblemStall:
		return "stall  "
	default:
		return "       "
	}
}

// RenderProblems prints the problems list as plain text (the CLI problems view).
func (d Display) RenderProblems(w io.Writer, problems []Problem) error {
	if len(problems) == 0 {
		fmt.Fprintln(w, d.Green("no problems detected in this session"))
		return nil
	}
	fmt.Fprintln(w, d.Bold("Problems")+d.Dim(fmt.Sprintf(" (%d)", len(problems))))
	fmt.Fprintln(w)
	for _, p := range problems {
		fmt.Fprintln(w, d.ProblemRow(p))
	}
	return nil
}
