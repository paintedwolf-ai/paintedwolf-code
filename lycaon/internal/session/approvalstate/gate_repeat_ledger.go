package approvalstate

import (
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/scopedstore"
)

// repeatSubjectCap bounds prior-subject labels on the wire repeat payload.
const repeatSubjectCap = 8

// RepeatSnapshot is the per-chat repeat state for one reason key after an ask.
type RepeatSnapshot struct {
	ReasonKey string
	Count     int
	// Asks is how many cards this reason has raised since the last user turn,
	// this one included. Count is distinct subjects; Asks is repetitions.
	Asks              int
	Subjects          []string
	SubjectsTruncated bool
	Suppressed        int
}

// GateRepeatLedger counts distinct approval subjects per reason key per chat,
// plus process-lifetime totals for Settings. Ephemeral and in-memory only.
type GateRepeatLedger struct {
	mu          sync.Mutex
	byChat      scopedstore.LRU[*repeatLedgerGuards]
	sinceLaunch map[string]int
}

type repeatLedgerGuards struct {
	byReason map[string]*repeatReasonState
}

type repeatReasonState struct {
	subjectOrder []string
	seen         map[string]struct{}
	asks         int
	suppressed   int
	truncated    bool
}

// NewGateRepeatLedger constructs empty repeat state.
func NewGateRepeatLedger() *GateRepeatLedger {
	return &GateRepeatLedger{
		sinceLaunch: make(map[string]int),
	}
}

func (l *GateRepeatLedger) guards(chatSessionID string) *repeatLedgerGuards {
	chatSessionID = normalizeChat(chatSessionID)
	g, ok := l.byChat.Load(chatSessionID)
	if !ok {
		g = &repeatLedgerGuards{byReason: make(map[string]*repeatReasonState)}
		l.byChat.Store(chatSessionID, g)
	}
	return g
}

func (l *GateRepeatLedger) reasonState(chatSessionID, reasonKey string) *repeatReasonState {
	g := l.guards(chatSessionID)
	state, ok := g.byReason[reasonKey]
	if !ok {
		state = &repeatReasonState{seen: make(map[string]struct{})}
		g.byReason[reasonKey] = state
	}
	return state
}

func (l *GateRepeatLedger) bumpSinceLaunch(reasonKey string) {
	if reasonKey == "" {
		return
	}
	l.sinceLaunch[reasonKey]++
}

func (state *repeatReasonState) noteSubject(subject string) bool {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return false
	}
	if _, ok := state.seen[subject]; ok {
		return false
	}
	state.seen[subject] = struct{}{}
	state.subjectOrder = append(state.subjectOrder, subject)
	if len(state.subjectOrder) > repeatSubjectCap {
		state.truncated = true
	}
	return true
}

func (state *repeatReasonState) count() int {
	return len(state.seen)
}

func (state *repeatReasonState) snapshot(reasonKey, currentSubject string) RepeatSnapshot {
	currentSubject = strings.TrimSpace(currentSubject)
	subjects := make([]string, 0, len(state.subjectOrder))
	for _, label := range state.subjectOrder {
		if label != currentSubject {
			subjects = append(subjects, label)
		}
	}
	if len(subjects) > repeatSubjectCap {
		subjects = subjects[:repeatSubjectCap]
	}
	return RepeatSnapshot{
		ReasonKey:         reasonKey,
		Count:             state.count(),
		Asks:              state.asks,
		Subjects:          subjects,
		SubjectsTruncated: state.truncated || len(state.subjectOrder) > repeatSubjectCap+1,
		Suppressed:        state.suppressed,
	}
}

// NoteAsk records a minted tool_approval ask for chat/reason/subject.
func (l *GateRepeatLedger) NoteAsk(chatSessionID, reasonKey, subject string) RepeatSnapshot {
	if l == nil {
		return RepeatSnapshot{ReasonKey: reasonKey}
	}
	reasonKey = strings.TrimSpace(reasonKey)
	subject = strings.TrimSpace(subject)
	if reasonKey == "" {
		return RepeatSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.reasonState(chatSessionID, reasonKey)
	state.noteSubject(subject)
	state.asks++
	l.bumpSinceLaunch(reasonKey)
	return state.snapshot(reasonKey, subject)
}

// NoteSuppressed records a SkipDenied ask that never minted a card.
func (l *GateRepeatLedger) NoteSuppressed(chatSessionID, reasonKey, subject string) {
	if l == nil {
		return
	}
	reasonKey = strings.TrimSpace(reasonKey)
	subject = strings.TrimSpace(subject)
	if reasonKey == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.reasonState(chatSessionID, reasonKey)
	state.noteSubject(subject)
	state.suppressed++
	l.bumpSinceLaunch(reasonKey)
}

// CountsSinceLaunch returns process-lifetime ask counts keyed by reason key.
func (l *GateRepeatLedger) CountsSinceLaunch() map[string]int {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]int, len(l.sinceLaunch))
	for key, count := range l.sinceLaunch {
		out[key] = count
	}
	return out
}

// NoteUserIntentBoundary clears per-chat repeat counts on a new user turn.
func (l *GateRepeatLedger) NoteUserIntentBoundary(chatSessionID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	g := l.guards(chatSessionID)
	g.byReason = make(map[string]*repeatReasonState)
}

// ForgetSession drops all repeat state for the chat.
func (l *GateRepeatLedger) ForgetSession(chatSessionID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byChat.Delete(normalizeChat(chatSessionID))
}
