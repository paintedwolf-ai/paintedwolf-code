package turnload

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// Source says why a tool stands loaded.
type Source string

const (
	// SourcePredicted came from a turn decision.
	SourcePredicted Source = "predicted"
	// SourceRequested came from request_tools.
	SourceRequested Source = "requested"
	// SourceCompanion loaded with a predicted tool that declares it.
	SourceCompanion Source = "companion"
)

// Load is one tool in a chat's standing set and why it is there.
type Load struct {
	Tool   string  `json:"tool"`
	Source Source  `json:"source"`
	P      float64 `json:"p,omitempty"`
	// Need is the request_tools text that loaded the tool.
	Need string `json:"need,omitempty"`
	// With is the predicted tool a companion loaded with.
	With string `json:"with,omitempty"`
	// Turn is the message that opened the turn the tool joined on.
	Turn string `json:"turn,omitempty"`
}

// Standing is the durable state of a chat's standing surface: the tools the
// host offers beyond the floor, the instruction units it leaves out, and the
// history epoch the current turn opened on. Every receipt carries it, so a
// restart restores the surface the provider last cached.
type Standing struct {
	Skill *SkillPreload `json:"skill,omitempty"`
	// Pointer is the skill the turn was told about in one line.
	Pointer *SkillRank `json:"pointer,omitempty"`
	// ToolEvent is the loadable tool whose first call already asked for a
	// skill this turn; a turn asks once.
	ToolEvent string   `json:"tool_event,omitempty"`
	Tools     []Load   `json:"tools"`
	Omitted   []string `json:"omitted,omitempty"`
	// HistoryEpoch identifies the compaction view the turn's history read.
	HistoryEpoch string `json:"history_epoch,omitempty"`
}

type sessionLoads struct {
	preload *SkillPreload
	pointer *SkillRank
	// request is the current turn's request in its author's words, which a
	// tool event ranks skills against; empty after a restart.
	request string
	// toolEvent names the loadable tool that claimed this turn's skill read.
	toolEvent string
	loadable  map[string]bool
	// standing is the tool set every call of the chat offers beyond the floor.
	standing map[string]Load
	// omitted are the instruction units the standing prompt leaves out.
	omitted map[string]bool
	// requested and used are what the chat has shown it needs; the engine
	// reads them as state.
	requested map[string]string
	used      map[string]bool
	epoch     string
	// openingMessageID is the user message that opened the current turn, so
	// every receipt the turn writes names the turn it belongs to.
	openingMessageID string
	// kind is the current turn's decided kind, empty when undecided.
	kind string
}

// Ledger holds every session's standing surface: the schema activation store
// request_tools writes and the prompt loop reads. The surface is part of the
// cached prefix, so tools leave and omissions widen only at a cold boundary.
type Ledger struct {
	mu       sync.RWMutex
	sessions map[string]*sessionLoads
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{sessions: make(map[string]*sessionLoads)}
}

func (l *Ledger) session(id string) *sessionLoads {
	id = strings.TrimSpace(id)
	s := l.sessions[id]
	if s == nil {
		s = &sessionLoads{loadable: map[string]bool{}, standing: map[string]Load{}, omitted: map[string]bool{}, requested: map[string]string{}, used: map[string]bool{}}
		l.sessions[id] = s
	}
	return s
}

// Active returns the standing tool names for a session. It satisfies the
// schema activation view the prompt loop and request_tools consume.
func (l *Ledger) Active(sessionID string) map[string]bool {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := l.sessions[strings.TrimSpace(sessionID)]
	if s == nil || len(s.standing) == 0 {
		return nil
	}
	out := make(map[string]bool, len(s.standing))
	for name := range s.standing {
		out[name] = true
	}
	return out
}

// Activate records tools request_tools loaded for need; they stand until the
// next cold boundary.
func (l *Ledger) Activate(sessionID string, names []string, need string) {
	if l == nil || strings.TrimSpace(sessionID) == "" || len(names) == 0 {
		return
	}
	need = strings.TrimSpace(need)
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.session(sessionID)
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			s.requested[name] = need
			if _, standing := s.standing[name]; !standing {
				s.standing[name] = Load{Tool: name, Source: SourceRequested, Need: need, Turn: s.openingMessageID}
			}
		}
	}
}

// Use records a tool the chat called.
func (l *Ledger) Use(sessionID, name string) {
	if l == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(name) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.session(sessionID).used[strings.TrimSpace(name)] = true
}

// Needed lists the tools the chat requested or called, sorted.
func (l *Ledger) Needed(sessionID string) []string {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := l.sessions[strings.TrimSpace(sessionID)]
	if s == nil {
		return nil
	}
	seen := make(map[string]bool, len(s.requested)+len(s.used))
	for name := range s.requested {
		seen[name] = true
	}
	for name := range s.used {
		seen[name] = true
	}
	return sortedKeys(seen)
}

// TurnOpening is what the host established before a turn's decision is
// applied.
type TurnOpening struct {
	// OpeningMessageID is the user message that opened the turn.
	OpeningMessageID string
	// Request is the turn's request in its author's words, bounded as the
	// engine reads it.
	Request string
	// Loadable names the tools the turn could load beyond its floor.
	Loadable []string
	Boundary Boundary
	// HistoryEpoch identifies the compaction view the turn's history reads.
	HistoryEpoch string
}

// open resets the per-turn state every kind of turn shares.
func (s *sessionLoads) open(opening TurnOpening) {
	s.openingMessageID = strings.TrimSpace(opening.OpeningMessageID)
	s.request = strings.TrimSpace(opening.Request)
	s.loadable = make(map[string]bool, len(opening.Loadable))
	for _, name := range opening.Loadable {
		if name = strings.TrimSpace(name); name != "" {
			s.loadable[name] = true
		}
	}
	s.epoch = opening.HistoryEpoch
	s.preload = nil
	s.pointer = nil
	s.toolEvent = ""
}

// BeginTurn applies a turn decision to the standing surface. Cold, the
// decision replaces the standing tools and omissions. Warm, its predictions
// join the standing tools and an omission holds only while every turn since
// the last cold boundary agreed with it. Abstained, the tools stay and every
// unit renders.
func (l *Ledger) BeginTurn(sessionID string, opening TurnOpening, decision Decision) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.session(sessionID)
	s.open(opening)
	cold := opening.Boundary.Cold()
	standing := make(map[string]Load, len(s.standing)+len(decision.Tools))
	if !cold || decision.Abstained {
		for name, load := range s.standing {
			if s.loadable[name] {
				standing[name] = load
			}
		}
	}
	for tool, p := range decision.Tools {
		if !s.loadable[tool] {
			continue
		}
		if _, kept := standing[tool]; !kept {
			standing[tool] = Load{Tool: tool, Source: SourcePredicted, P: p, Turn: s.openingMessageID}
		}
	}
	// A predicted tool's declared companions load with it, when the surface offers them.
	for tool := range decision.Tools {
		if !s.loadable[tool] {
			continue
		}
		for _, companion := range toolcontract.WithCompanions([]string{tool})[1:] {
			if _, kept := standing[companion]; !kept && s.loadable[companion] {
				standing[companion] = Load{Tool: companion, Source: SourceCompanion, With: tool, Turn: s.openingMessageID}
			}
		}
	}
	s.standing = standing
	omitted := make(map[string]bool, len(decision.Omitted))
	if !decision.Abstained {
		for id := range decision.Omitted {
			if cold || s.omitted[id] {
				omitted[id] = true
			}
		}
	}
	s.omitted = omitted
	s.kind = decision.Kind
}

// BeginUndecidedTurn opens a turn whose workflow takes no turn decisions. The
// tools the chat requested stay while the turn can load them; predicted tools
// leave only at cold boundaries, and every instruction unit renders.
func (l *Ledger) BeginUndecidedTurn(sessionID string, opening TurnOpening) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.session(sessionID)
	s.open(opening)
	standing := make(map[string]Load, len(s.standing))
	for name, load := range s.standing {
		if s.loadable[name] && (load.Source == SourceRequested || !opening.Boundary.Cold()) {
			standing[name] = load
		}
	}
	s.standing = standing
	s.omitted = map[string]bool{}
	s.kind = ""
}

// ClaimToolEvent gives one loadable tool's first call the turn's skill read.
// It returns the turn's request when the tool could load on this turn, no
// tool has claimed the turn yet, and no skill is already read; the claim
// holds for the rest of the turn whatever the ranking then decides.
func (l *Ledger) ClaimToolEvent(sessionID, tool string) (request string, ok bool) {
	if l == nil {
		return "", false
	}
	tool = strings.TrimSpace(tool)
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sessions[strings.TrimSpace(sessionID)]
	if s == nil || tool == "" || s.request == "" || s.toolEvent != "" || s.preload != nil || !s.loadable[tool] {
		return "", false
	}
	s.toolEvent = tool
	return s.request, true
}

// SetPointer records the skill the turn is told about in one line.
func (l *Ledger) SetPointer(sessionID string, pointer *SkillRank) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.session(sessionID).pointer = clonePointer(pointer)
}

// Pointer returns the skill the turn is told about in one line, if any.
func (l *Ledger) Pointer(sessionID string) *SkillRank {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if s := l.sessions[strings.TrimSpace(sessionID)]; s != nil {
		return clonePointer(s.pointer)
	}
	return nil
}

func clonePointer(p *SkillRank) *SkillRank {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

// TurnMessageID returns the user message that opened the session's current
// turn, or empty before any turn began.
func (l *Ledger) TurnMessageID(sessionID string) string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if s := l.sessions[strings.TrimSpace(sessionID)]; s != nil {
		return s.openingMessageID
	}
	return ""
}

// TurnKind returns the kind decided for the session's current turn, or empty
// when the turn took no decision or the decision fell below its floor.
func (l *Ledger) TurnKind(sessionID string) string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if s := l.sessions[strings.TrimSpace(sessionID)]; s != nil {
		return s.kind
	}
	return ""
}

// Loadable reports whether the current turn could load tool beyond its floor.
func (l *Ledger) Loadable(sessionID, tool string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := l.sessions[strings.TrimSpace(sessionID)]
	return s != nil && s.loadable[strings.TrimSpace(tool)]
}

// Omitted returns the instruction units the standing prompt leaves out.
func (l *Ledger) Omitted(sessionID string) map[string]bool {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := l.sessions[strings.TrimSpace(sessionID)]
	if s == nil || len(s.omitted) == 0 {
		return nil
	}
	out := make(map[string]bool, len(s.omitted))
	for id := range s.omitted {
		out[id] = true
	}
	return out
}

// Standing returns the session's standing surface.
func (l *Ledger) Standing(sessionID string) Standing {
	if l == nil {
		return Standing{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := l.sessions[strings.TrimSpace(sessionID)]
	if s == nil {
		return Standing{}
	}
	return s.standingRecord()
}

func (s *sessionLoads) standingRecord() Standing {
	tools := make([]Load, 0, len(s.standing))
	for _, load := range s.standing {
		tools = append(tools, load)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Tool < tools[j].Tool })
	return Standing{Tools: tools, Omitted: sortedKeys(s.omitted), HistoryEpoch: s.epoch, Skill: clonePreload(s.preload), Pointer: clonePointer(s.pointer), ToolEvent: s.toolEvent}
}

func sortedKeys[V any](set map[string]V) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Forget drops a session's loads.
func (l *Ledger) Forget(sessionID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sessions, strings.TrimSpace(sessionID))
}

// Restore rebuilds a session from its latest receipt's standing surface and
// from what history shows the chat needed. Compacted history is only a
// suffix, so the receipt carries the surface itself.
func (l *Ledger) Restore(sessionID string, recorded Standing, history []api.Message) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	requested, used := replayLoads(history)
	standing := make(map[string]Load, len(recorded.Tools))
	for _, load := range recorded.Tools {
		name := strings.TrimSpace(load.Tool)
		if name == "" || (load.Source != SourcePredicted && load.Source != SourceRequested && load.Source != SourceCompanion) {
			continue
		}
		load.Tool = name
		standing[name] = load
		if load.Source == SourceRequested {
			if _, ok := requested[name]; !ok {
				requested[name] = load.Need
			}
		}
	}
	omitted := make(map[string]bool, len(recorded.Omitted))
	for _, id := range recorded.Omitted {
		if id = strings.TrimSpace(id); id != "" {
			omitted[id] = true
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.session(sessionID)
	s.standing = standing
	s.omitted = omitted
	s.epoch = recorded.HistoryEpoch
	s.preload = clonePreload(recorded.Skill)
	s.pointer = clonePointer(recorded.Pointer)
	s.toolEvent = recorded.ToolEvent
	s.requested = requested
	s.used = used
}

// RequestToolsResult is the request_tools payload the ledger replays.
type RequestToolsResult struct {
	Need          string     `json:"need"`
	Discovery     *Discovery `json:"discovery,omitempty"`
	Loaded        []string   `json:"loaded"`
	AlreadyLoaded []string   `json:"already_loaded,omitempty"`
	// Nearest names the loaded tools that were the closest matches rather
	// than confident ones; their descriptions decide whether they fit.
	Nearest []string `json:"nearest,omitempty"`
	Note    string   `json:"note,omitempty"`
}

func replayLoads(history []api.Message) (requested map[string]string, used map[string]bool) {
	requested = map[string]string{}
	used = map[string]bool{}
	calls := make(map[string]string)
	for _, message := range history {
		if message.Role == api.MessageRoleAssistant {
			for _, call := range message.ToolCalls {
				calls[call.ID] = message.ID
			}
			continue
		}
		result := message.ToolResult
		if message.Role != api.MessageRoleTool || result == nil || result.Outcome != api.ToolResultOutcomeCompleted {
			continue
		}
		if result.ToolCallID == "" || calls[result.ToolCallID] == "" || calls[result.ToolCallID] != result.AssistantMessageID {
			continue
		}
		if result.Tool != "request_tools" {
			if name := strings.TrimSpace(result.Tool); name != "" {
				used[name] = true
			}
			continue
		}
		receipt := result.Invocation
		if receipt == nil || receipt.Tool != "request_tools" || receipt.Owner != "tool_surface" || receipt.Status != api.InvocationStatusCompleted || !receipt.Invoked {
			continue
		}
		var payload RequestToolsResult
		if json.Unmarshal([]byte(result.Content), &payload) != nil {
			continue
		}
		for _, name := range append(payload.Loaded, payload.AlreadyLoaded...) {
			if name = strings.TrimSpace(name); name != "" {
				requested[name] = strings.TrimSpace(payload.Need)
			}
		}
	}
	return requested, used
}
