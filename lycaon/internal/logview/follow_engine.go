package logview

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"regexp"
	"sort"
	"strings"
	"time"
)

// EventKind tags a line in the live follow stream.
type EventKind string

const (
	EventSession EventKind = "session" // a coordinator session started
	EventSpawn   EventKind = "spawn"   // a worker was spawned
	EventSay     EventKind = "say"     // an agent's reasoning / prose output
	EventTool    EventKind = "tool"    // an agent called a tool (paired with its result)
	EventUser    EventKind = "user"    // a user prompt or host guidance nudge
	EventDone    EventKind = "done"    // a worker leg finished
	EventReject  EventKind = "reject"  // a turn was rejected
	EventStall   EventKind = "stall"   // a Den main-thread freeze (den-perf.jsonl)
)

// Event is one thing that happened, the unit of `logs follow`. For tool events the
// Summary is a human outcome ("scraper.py · 120 lines", "`pytest` · exit 0"). CallID
// and Turn locate the exact source so drilling in opens the precise item.
type Event struct {
	Time      time.Time `json:"ts"`
	Kind      EventKind `json:"kind"`
	Agent     string    `json:"agent,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	CallID    string    `json:"call_id,omitempty"` // tool_call id, to open the exact tool detail
	Turn      int       `json:"turn,omitempty"`    // the LLM iteration, to open the exact turn
	MsgID     string    `json:"msg_id,omitempty"`  // transcript message id, to scroll to the exact point
	Summary   string    `json:"summary"`
	Err       bool      `json:"err,omitempty"`
}

// FollowEngine turns appended capture records into a chronological event stream. It
// pairs each tool call with the result that follows it and dedupes emitted events by
// message ID, so it stays correct even though cumulative transcripts compact and
// re-appear across records.
type FollowEngine struct {
	seenSession map[string]bool
	seenMsg     map[string]bool // tool/reject message IDs already emitted (or baselined)
	primed      map[string]bool // a session whose backlog has been baselined
	emitAll     bool            // BuildEventLog: emit the whole history, not just new activity
}

// BuildEventLog produces the full chronological event stream for a capture (every
// agent's reasoning, tools, nudges, outcomes, and Den main-thread stalls) — the
// combined session timeline, static rather than tailed.
func BuildEventLog(sessions []SessionRecord, llm []LLMRecord, denPerf []DenPerfRecord) []Event {
	eng := NewFollowEngine()
	eng.emitAll = true
	out := eng.SessionEvents(sessions)
	out = append(out, eng.LLMEvents(llm)...)
	out = append(out, DenPerfEvents(denPerf)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

// DenPerfEvents turns stall lines into timeline events (correlated by timestamp).
func DenPerfEvents(recs []DenPerfRecord) []Event {
	var out []Event
	for _, r := range recs {
		if !r.IsStall() {
			continue
		}
		ms := r.StallMS()
		summary := r.Event
		if ms > 0 {
			summary = fmt.Sprintf("%s · %dms", r.Event, ms)
		}
		if recent := r.Recent(); recent != "" {
			summary = fmt.Sprintf("%s · %s", summary, truncate(recent, 48))
		}
		out = append(out, Event{
			Time:    r.TS,
			Kind:    EventStall,
			Summary: summary,
			Err:     ms >= 500,
		})
	}
	return out
}

// NewFollowEngine starts an engine with no history. The first record seen for a
// session establishes a baseline (its backlog is marked seen but not emitted) so the
// stream only shows activity from the moment following begins, like tail -f.
func NewFollowEngine() *FollowEngine {
	return &FollowEngine{
		seenSession: map[string]bool{},
		seenMsg:     map[string]bool{},
		primed:      map[string]bool{},
	}
}

var (
	reAgentType = regexp.MustCompile(`agent_type="([^"]+)"`)
	reToolTag   = regexp.MustCompile(`^\[([a-z_]+)#\d+\]`)
)

// SessionEvents emits a session/spawn event for each newly-created session.
func (e *FollowEngine) SessionEvents(recs []SessionRecord) []Event {
	var out []Event
	for _, r := range recs {
		if e.seenSession[r.SessionID] {
			continue
		}
		e.seenSession[r.SessionID] = true
		if r.ParentSessionID == "" {
			out = append(out, Event{Time: r.TS, Kind: EventSession, Agent: r.AgentType, SessionID: r.SessionID,
				Summary: "started: " + truncate(cleanTask(r.Task), 80)})
		} else {
			out = append(out, Event{Time: r.TS, Kind: EventSpawn, Agent: r.AgentType, SessionID: r.SessionID,
				Summary: spawnSummary(r.Task)})
		}
	}
	return out
}

func spawnSummary(task string) string {
	if s := scopeDescriptor(task); s != "" {
		return s
	}
	if t := cleanTask(task); t != "" {
		return truncate(t, 64)
	}
	return "spawned"
}

// LLMEvents walks each record's transcript, pairing assistant tool calls with the
// results that follow them, and emits each tool/reject exactly once (keyed by
// message ID). The first record for a session is baselined — its messages are marked
// seen but not emitted.
func (e *FollowEngine) LLMEvents(recs []LLMRecord) []Event {
	var out []Event
	for _, rec := range recs {
		sid := rec.SessionID
		baseline := !e.primed[sid] && !e.emitAll
		e.primed[sid] = true

		var pending []LLMToolCall // calls seen in this walk, awaiting their result
		for _, m := range rec.Messages {
			ts := m.TS
			if ts.IsZero() {
				ts = rec.TS
			}
			switch m.Role {
			case "assistant":
				pending = append(pending, m.ToolCalls...)
				// The model's prose — its reasoning and conclusions — is a step too.
				if text := strings.TrimSpace(messageText(m.Content)); text != "" {
					if e.claim(m.ID) && !baseline {
						out = append(out, Event{Time: ts, Kind: EventSay, Agent: rec.AgentType, SessionID: sid,
							Turn: rec.Iteration, MsgID: m.ID, Summary: truncate(oneLine(text), 96)})
					}
				}
			case "tool":
				var call *LLMToolCall
				if len(pending) > 0 { // consume in lockstep to keep call↔result alignment
					c := pending[0]
					pending = pending[1:]
					call = &c
				}
				if !e.claim(m.ID) || baseline {
					continue
				}
				out = append(out, toolEvent(rec.AgentType, sid, rec.Iteration, m.ID, ts, call, messageText(m.Content)))
			case "user":
				if ev, ok := userEvent(rec.AgentType, sid, rec.Iteration, m.ID, ts, messageText(m.Content)); ok {
					if e.claim(m.ID) && !baseline {
						out = append(out, ev)
					}
				}
			}
		}
	}
	return out
}

// claim marks a message ID as handled, returning false if already seen. Messages
// without an ID are always treated as new (can't dedupe).
func (e *FollowEngine) claim(id string) bool {
	if id == "" {
		return true
	}
	if e.seenMsg[id] {
		return false
	}
	e.seenMsg[id] = true
	return true
}

// toolEvent builds a tool (or worker-done) event from a result message and its
// paired call.
func toolEvent(agent, sid string, iter int, msgID string, ts time.Time, call *LLMToolCall, content string) Event {
	if child := reChildID.FindStringSubmatch(content); child != nil {
		o := classifyOutcome(firstSubmatch(reState, content), firstSubmatch(reLegStatus, content))
		return Event{Time: ts, Kind: EventDone, Agent: firstSubmatch(reAgentType, content), SessionID: child[1],
			Turn: iter, MsgID: msgID, Summary: "leg " + string(orStatus(o.Status)),
			Err: o.Status == StatusFailed || o.Status == StatusPartial}
	}
	name, args, callID := "", json.RawMessage(nil), ""
	if call != nil {
		name, args, callID = call.Name, call.Args, call.ID
	}
	if name == "" {
		if m := reToolTag.FindStringSubmatch(content); m != nil {
			name = m[1]
		}
	}
	summary, failed := toolSummary(name, args, content)
	return Event{Time: ts, Kind: EventTool, Agent: agent, SessionID: sid, Tool: orDash(name),
		CallID: callID, Turn: iter, MsgID: msgID, Summary: summary, Err: failed}
}

// userEvent classifies a user-role message: a rejection, a real user prompt, or a
// host guidance nudge. Pure host loop bookkeeping (wakes, closeouts) is skipped.
func userEvent(agent, sid string, iter int, msgID string, ts time.Time, content string) (Event, bool) {
	t := strings.TrimSpace(content)
	switch {
	case t == "":
		return Event{}, false
	case strings.HasPrefix(t, hostmarker.Rejected):
		return Event{Time: ts, Kind: EventReject, Agent: agent, SessionID: sid, Turn: iter, MsgID: msgID,
			Summary: rejectSummaryText(t), Err: true}, true
	case strings.HasPrefix(t, "[host:"):
		return Event{}, false // loop-wake / worker-closeout bookkeeping
	default:
		// A guidance nudge (">>> …") or a real user prompt — both are steering input.
		return Event{Time: ts, Kind: EventUser, Agent: agent, SessionID: sid, Turn: iter, MsgID: msgID,
			Summary: truncate(oneLine(strings.TrimPrefix(t, ">>> ")), 96)}, true
	}
}

func orStatus(s OutcomeStatus) OutcomeStatus {
	if s == StatusUnknown {
		return "finished"
	}
	return s
}

func rejectSummaryText(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Code:") {
			return strings.TrimSpace(line)
		}
	}
	return truncate(oneLine(text), 70)
}

// EventLine renders one event as a parsable, fixed-column line. The action column
// shows the tool name for tool events (read/command/scan…) or the event kind otherwise,
// so a developer can scan what each agent is doing and how it turned out.
func (d Display) EventLine(e Event) string {
	action := string(e.Kind)
	if e.Kind == EventTool && e.Tool != "" {
		action = e.Tool
	}
	summary := d.plainText(e.Summary)
	switch {
	case e.Err:
		summary = d.Red(summary)
	case e.Kind == EventSay:
		summary = d.Dim(summary) // reasoning is supporting narrative
	}
	return fmt.Sprintf("%s %s %s %s",
		d.Dim(d.time(e.Time)),
		d.eventAction(e, action),
		d.BoldCyan(fmt.Sprintf("%-16s", truncate(orDash(e.Agent), 16))),
		summary)
}

func (d Display) eventAction(e Event, action string) string {
	tag := fmt.Sprintf("%-13s", truncate(action, 13))
	if e.Err {
		return d.Red(tag)
	}
	switch e.Kind {
	case EventReject:
		return d.Red(tag)
	case EventDone:
		return d.Green(tag)
	case EventTool:
		return d.Yellow(tag)
	case EventStall:
		return d.Yellow(tag)
	case EventSession, EventSpawn:
		return d.Cyan(tag)
	case EventUser:
		return d.Magenta(tag)
	case EventSay:
		return d.Dim(tag)
	default:
		return d.Dim(tag)
	}
}
