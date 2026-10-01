package logview

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// OutcomeStatus is the derived terminal state of an agent's work.
type OutcomeStatus string

const (
	StatusUnknown  OutcomeStatus = ""
	StatusComplete OutcomeStatus = "complete"
	StatusPartial  OutcomeStatus = "partial"
	StatusBlocked  OutcomeStatus = "blocked"
	StatusFailed   OutcomeStatus = "failed"
	StatusRunning  OutcomeStatus = "running"
)

// Outcome is what became of an agent's leg, derived from the coordinator's task
// result envelope (authoritative) or, failing that, the agent's own final turn.
type Outcome struct {
	Status OutcomeStatus
	Detail string // short human reason, e.g. "forced final turn" or "leg partial"
}

// Agent is one session in the capture (coordinator or worker) with its ordered
// turns and place in the topology.
type Agent struct {
	SessionID string
	ParentID  string
	AgentType string
	Surface   string
	Task      string // raw task brief (worker assignment or user request)
	Turns     []LLMRecord
	Children  []*Agent
	Outcome   Outcome
	Depth     int
}

// IsCoordinator reports whether this is the root session driving the others.
func (a *Agent) IsCoordinator() bool { return a.ParentID == "" }

// TurnIndexByIteration returns the position of the turn with the given iteration in
// the agent's turns, or -1.
func (a *Agent) TurnIndexByIteration(iter int) int {
	for i := range a.Turns {
		if a.Turns[i].Iteration == iter {
			return i
		}
	}
	return -1
}

// TurnIndexByMessageID returns the position of the turn whose transcript contains the
// message with the given ID, or -1. This is the precise locator for drilling from a
// timeline event: a single iteration can span several LLM records (different surfaces),
// so matching the record that actually holds the message is unambiguous where matching
// by iteration is not.
func (a *Agent) TurnIndexByMessageID(msgID string) int {
	if msgID == "" {
		return -1
	}
	for i := range a.Turns {
		for _, m := range a.Turns[i].Messages {
			if m.ID == msgID {
				return i
			}
		}
	}
	return -1
}

// PromptTokens sums prompt tokens across the agent's turns.
func (a *Agent) PromptTokens() int {
	total := 0
	for _, t := range a.Turns {
		if t.Usage != nil {
			total += t.Usage.PromptTokens
		}
	}
	return total
}

// SessionTree is the whole capture organized as the agent topology, the entry
// point for the session-narrative views.
type SessionTree struct {
	Root   *Agent   // coordinator; nil for an empty capture
	Agents []*Agent // depth-first order (coordinator, then each worker)
	byID   map[string]*Agent
}

// Find returns an agent by session ID.
func (t *SessionTree) Find(sessionID string) (*Agent, bool) {
	a, ok := t.byID[sessionID]
	return a, ok
}

// Roots returns the coordinator sessions in start order (a capture from a
// long-running sidecar can hold several).
func (t *SessionTree) Roots() []*Agent {
	var roots []*Agent
	for _, a := range t.Agents {
		if a.IsCoordinator() {
			roots = append(roots, a)
		}
	}
	return roots
}

// Headline is the user's request that started the session.
func (t *SessionTree) Headline() string {
	if t.Root == nil {
		return ""
	}
	if task := cleanTask(t.Root.Task); task != "" {
		return task
	}
	// Fall back to the first user message of the coordinator's first turn.
	if len(t.Root.Turns) > 0 {
		for _, m := range t.Root.Turns[0].Messages {
			if m.Role == "user" {
				return oneLine(messageText(m.Content))
			}
		}
	}
	return "(session)"
}

// BuildSessionTree groups LLM turns by session, links them via the topology rows,
// and derives each worker's outcome.
func BuildSessionTree(sessions []SessionRecord, llm []LLMRecord) *SessionTree {
	tree := &SessionTree{byID: map[string]*Agent{}}

	ensure := func(id string) *Agent {
		a, ok := tree.byID[id]
		if !ok {
			a = &Agent{SessionID: id}
			tree.byID[id] = a
		}
		return a
	}

	for _, s := range sessions {
		a := ensure(s.SessionID)
		a.ParentID = s.ParentSessionID
		a.AgentType = s.AgentType
		a.Surface = s.Surface
		if strings.TrimSpace(s.Task) != "" {
			a.Task = s.Task
		}
	}
	for _, rec := range llm {
		if rec.SessionID == "" {
			continue
		}
		a := ensure(rec.SessionID)
		if a.AgentType == "" {
			a.AgentType = rec.AgentType
		}
		if a.Surface == "" {
			a.Surface = rec.Surface
		}
		a.Turns = append(a.Turns, rec)
	}
	for _, a := range tree.byID {
		sort.SliceStable(a.Turns, func(i, j int) bool {
			if a.Turns[i].Iteration != a.Turns[j].Iteration {
				return a.Turns[i].Iteration < a.Turns[j].Iteration
			}
			return a.Turns[i].TS.Before(a.Turns[j].TS)
		})
	}

	outcomes := deriveWorkerOutcomes(tree)
	linkTopology(tree)
	for _, a := range tree.Agents {
		if a.IsCoordinator() {
			continue
		}
		if o, ok := outcomes[a.SessionID]; ok {
			a.Outcome = o
		} else {
			a.Outcome = fallbackOutcome(a)
		}
	}
	return tree
}

// linkTopology wires parent/child pointers and produces the depth-first Agents list.
func linkTopology(tree *SessionTree) {
	var roots []*Agent
	for _, a := range tree.byID {
		if a.ParentID == "" {
			roots = append(roots, a)
			continue
		}
		if parent, ok := tree.byID[a.ParentID]; ok {
			parent.Children = append(parent.Children, a)
		} else {
			roots = append(roots, a) // orphaned child still shows
		}
	}
	stableByStart := func(xs []*Agent) {
		sort.SliceStable(xs, func(i, j int) bool {
			return agentStart(xs[i]).Before(agentStart(xs[j]))
		})
	}
	stableByStart(roots)
	for _, a := range tree.byID {
		stableByStart(a.Children)
	}
	if len(roots) > 0 {
		tree.Root = roots[0]
	}
	var walk func(a *Agent, depth int)
	walk = func(a *Agent, depth int) {
		a.Depth = depth
		tree.Agents = append(tree.Agents, a)
		for _, c := range a.Children {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
}

func agentStart(a *Agent) time.Time {
	if len(a.Turns) > 0 {
		return a.Turns[0].TS
	}
	return time.Time{}
}

var (
	reChildID   = regexp.MustCompile(`child_session_id="([^"]+)"`)
	reState     = regexp.MustCompile(`state="([a-zA-Z]+)"`)
	reLegStatus = regexp.MustCompile(`(?:"leg_status":\s*"|Leg status:\s*)([a-zA-Z_]+)`)
)

// ChildSessionsIn returns referenced worker session IDs in first-seen order.
func ChildSessionsIn(r LLMRecord) []string {
	seen := map[string]bool{}
	var ids []string
	for _, m := range r.Messages {
		if m.Role != "tool" {
			continue
		}
		for _, match := range reChildID.FindAllStringSubmatch(messageText(m.Content), -1) {
			id := match[1]
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// deriveWorkerOutcomes parses the coordinator's task-result envelopes for each
// child session's terminal state and leg status.
func deriveWorkerOutcomes(tree *SessionTree) map[string]Outcome {
	out := map[string]Outcome{}
	for _, a := range tree.byID {
		for _, turn := range a.Turns {
			for _, m := range turn.Messages {
				if m.Role != "tool" {
					continue
				}
				content := messageText(m.Content)
				loc := reChildID.FindStringSubmatchIndex(content)
				if loc == nil {
					continue
				}
				childID := content[loc[2]:loc[3]]
				// Scope state/leg_status to the envelope starting at this child id.
				rest := content[loc[0]:]
				out[childID] = classifyOutcome(
					firstSubmatch(reState, rest),
					firstSubmatch(reLegStatus, rest),
				)
			}
		}
	}
	return out
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

func classifyOutcome(state, leg string) Outcome {
	switch {
	case state == "failed" || state == "error":
		return Outcome{Status: StatusFailed, Detail: "leg " + orWord(leg, "failed")}
	case leg == "blocked":
		return Outcome{Status: StatusBlocked, Detail: "leg blocked"}
	case leg == "partial":
		return Outcome{Status: StatusPartial, Detail: "leg partial"}
	case leg == "complete" || state == "complete":
		return Outcome{Status: StatusComplete, Detail: "leg complete"}
	case state == "running" || state == "active":
		return Outcome{Status: StatusRunning, Detail: "still running"}
	default:
		return Outcome{Status: StatusUnknown}
	}
}

// fallbackOutcome infers a worker's state from its own final turn when the
// coordinator envelope was not captured.
func fallbackOutcome(a *Agent) Outcome {
	if len(a.Turns) == 0 {
		return Outcome{Status: StatusRunning, Detail: "no turns captured"}
	}
	last := a.Turns[len(a.Turns)-1]
	for i := len(last.Messages) - 1; i >= 0; i-- {
		m := last.Messages[i]
		if m.Role != "user" {
			continue
		}
		if strings.Contains(messageText(m.Content), "[host:worker-closeout]") {
			return Outcome{Status: StatusPartial, Detail: "forced final turn"}
		}
		break
	}
	return Outcome{Status: StatusUnknown}
}

func orWord(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
