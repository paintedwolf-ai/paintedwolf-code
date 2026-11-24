package completioncue

import (
	"context"
	"strings"
	"sync"
)

const (
	// CodeOutcomeCoverage is the OAR hint code for mid-plan coverage checks.
	CodeOutcomeCoverage = "PLAN_OUTCOME_COVERAGE"
	// CodeFinalItemCoverage is the OAR hint code before the last open plan item.
	CodeFinalItemCoverage = "PLAN_FINAL_ITEM_COVERAGE"
)

// DeliveredState tracks which cues have already fired for one user intent.
type DeliveredState struct {
	IntentID           string
	OutcomeDelivered   bool
	FinalItemDelivered bool
}

// Store persists per-session delivered cue bits across compaction and restart.
type Store interface {
	GetCompletionCueState(ctx context.Context, sessionID string) (DeliveredState, error)
	SaveCompletionCueState(ctx context.Context, sessionID string, state DeliveredState) error
}

// CheckFact captures one terminal passing source check attributed at launch.
type CheckFact struct {
	CheckID          string
	SessionID        string
	WorkflowRunID    string
	IntentID         string
	SourceRevision   string
	SourceRootDigest string
}

// SelectInput supplies current decision facts for cue selection.
type SelectInput struct {
	SessionID           string
	WorkflowRunID       string
	IntentID            string
	OrdinaryDecision    bool
	PendingCount        func(ctx context.Context) (int, bool)
	ConfirmCheckCurrent func(ctx context.Context, check CheckFact) bool
}

// DecisionBinding binds one cue decision to a single model decision window.
type DecisionBinding struct {
	SessionID string
	IntentID  string
	Code      string
	BoundSeq  uint64
}

type sessionWindow struct {
	seq                uint64
	completionSeq      uint64
	completionIntentID string
	completionRunID    string
	checkSeq           uint64
	pendingCheck       *CheckFact
	seenIntentID       string
	seenCheckIDs       map[string]bool
	deliveredLoaded    bool
	deliveredDirty     bool
	delivered          DeliveredState
}

// Tracker coordinates mid-work completion cues per coordinator session.
type Tracker struct {
	mu       sync.Mutex
	store    Store
	sessions map[string]*sessionWindow
}

// NewTracker constructs a Tracker with optional durable continuation persistence.
func NewTracker(store Store) *Tracker {
	return &Tracker{
		store:    store,
		sessions: make(map[string]*sessionWindow),
	}
}

// SetStore installs or replaces the durable continuation store.
func (t *Tracker) SetStore(store Store) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.store = store
}

// ForgetSession drops in-memory state for a deleted session.
func (t *Tracker) ForgetSession(sessionID string) {
	if t == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessions, sessionID)
}

func (t *Tracker) windowLocked(sessionID string) *sessionWindow {
	if t.sessions == nil {
		t.sessions = make(map[string]*sessionWindow)
	}
	win := t.sessions[sessionID]
	if win == nil {
		win = &sessionWindow{}
		t.sessions[sessionID] = win
	}
	return win
}

// RecordPlanItemCompleted records a committed pending-to-done checklist transition.
func (t *Tracker) RecordPlanItemCompleted(sessionID, workflowRunID, intentID string) {
	if t == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	intentID = strings.TrimSpace(intentID)
	workflowRunID = strings.TrimSpace(workflowRunID)
	if sessionID == "" || intentID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	win := t.windowLocked(sessionID)
	win.seq++
	win.completionSeq = win.seq
	win.completionIntentID = intentID
	win.completionRunID = workflowRunID
}

// RecordPassingCheck records a newly settled passing source check attributed at launch.
func (t *Tracker) RecordPassingCheck(fact CheckFact) {
	if t == nil {
		return
	}
	fact.CheckID = strings.TrimSpace(fact.CheckID)
	fact.SessionID = strings.TrimSpace(fact.SessionID)
	fact.IntentID = strings.TrimSpace(fact.IntentID)
	fact.WorkflowRunID = strings.TrimSpace(fact.WorkflowRunID)
	if fact.CheckID == "" || fact.SessionID == "" || fact.IntentID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	win := t.windowLocked(fact.SessionID)
	if win.seenIntentID != fact.IntentID {
		win.seenIntentID = fact.IntentID
		win.seenCheckIDs = make(map[string]bool)
	}
	if win.seenCheckIDs[fact.CheckID] {
		return
	}
	win.seenCheckIDs[fact.CheckID] = true
	copied := fact
	win.seq++
	win.checkSeq = win.seq
	win.pendingCheck = &copied
}

// SelectForDecision evaluates pending events against the current committed plan
// and returns a binding for this model decision.
func (t *Tracker) SelectForDecision(ctx context.Context, in SelectInput) DecisionBinding {
	if t == nil {
		return DecisionBinding{}
	}
	sessionID := strings.TrimSpace(in.SessionID)
	intentID := strings.TrimSpace(in.IntentID)
	workflowRunID := strings.TrimSpace(in.WorkflowRunID)
	if sessionID == "" {
		return DecisionBinding{}
	}

	delivered, boundSeq, hasCompletion, pendingCheck := t.snapshotForDecision(ctx, sessionID, workflowRunID, intentID)
	binding := DecisionBinding{
		SessionID: sessionID,
		IntentID:  intentID,
		BoundSeq:  boundSeq,
	}
	if !in.OrdinaryDecision || intentID == "" {
		return binding
	}

	eligible := hasCompletion
	if !eligible && pendingCheck != nil && in.ConfirmCheckCurrent != nil {
		eligible = in.ConfirmCheckCurrent(ctx, *pendingCheck)
	}
	if !eligible || in.PendingCount == nil {
		return binding
	}
	pendingCount, ok := in.PendingCount(ctx)
	if !ok || pendingCount <= 0 {
		return binding
	}

	switch {
	case pendingCount == 1:
		if !delivered.FinalItemDelivered {
			binding.Code = CodeFinalItemCoverage
		}
	case pendingCount >= 2:
		if !delivered.OutcomeDelivered && !delivered.FinalItemDelivered {
			binding.Code = CodeOutcomeCoverage
		}
	}
	return binding
}

func (t *Tracker) snapshotForDecision(
	ctx context.Context,
	sessionID, workflowRunID, intentID string,
) (DeliveredState, uint64, bool, *CheckFact) {
	t.mu.Lock()
	store := t.store
	win := t.windowLocked(sessionID)
	needLoad := !win.deliveredLoaded && store != nil
	t.mu.Unlock()

	var loaded DeliveredState
	if needLoad {
		if st, err := store.GetCompletionCueState(ctx, sessionID); err == nil {
			loaded = st
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	win = t.windowLocked(sessionID)
	if !win.deliveredLoaded {
		if needLoad {
			win.delivered = loaded
		}
		win.deliveredLoaded = true
	}
	if intentID != "" && win.delivered.IntentID != intentID {
		win.delivered = DeliveredState{IntentID: intentID}
		win.deliveredDirty = true
		if win.completionIntentID != intentID {
			win.completionSeq = 0
			win.completionIntentID = ""
			win.completionRunID = ""
		}
		if win.pendingCheck != nil && win.pendingCheck.IntentID != intentID {
			win.checkSeq = 0
			win.pendingCheck = nil
		}
		if win.seenIntentID != intentID {
			win.seenIntentID = intentID
			win.seenCheckIDs = nil
		}
	}
	hasCompletion := win.completionSeq > 0 &&
		win.completionIntentID == intentID &&
		win.completionRunID == workflowRunID
	var check *CheckFact
	if win.checkSeq > 0 && win.pendingCheck != nil &&
		win.pendingCheck.IntentID == intentID &&
		win.pendingCheck.WorkflowRunID == workflowRunID {
		copied := *win.pendingCheck
		check = &copied
	}
	return win.delivered, win.seq, hasCompletion, check
}

// CommitDecision marks any delivered cue durable and expires events up to BoundSeq.
func (t *Tracker) CommitDecision(ctx context.Context, binding DecisionBinding) error {
	if t == nil {
		return nil
	}
	sessionID := strings.TrimSpace(binding.SessionID)
	if sessionID == "" {
		return nil
	}

	t.mu.Lock()
	win := t.windowLocked(sessionID)
	if binding.BoundSeq > 0 {
		if win.completionSeq <= binding.BoundSeq {
			win.completionSeq = 0
			win.completionIntentID = ""
			win.completionRunID = ""
		}
		if win.checkSeq <= binding.BoundSeq {
			win.checkSeq = 0
			win.pendingCheck = nil
		}
	}
	intentID := strings.TrimSpace(binding.IntentID)
	if intentID != "" {
		if win.delivered.IntentID != intentID {
			win.delivered = DeliveredState{IntentID: intentID}
			win.deliveredDirty = true
		}
		switch binding.Code {
		case CodeOutcomeCoverage:
			if !win.delivered.OutcomeDelivered {
				win.delivered.OutcomeDelivered = true
				win.deliveredDirty = true
			}
		case CodeFinalItemCoverage:
			if !win.delivered.FinalItemDelivered {
				win.delivered.FinalItemDelivered = true
				win.deliveredDirty = true
			}
		}
		win.deliveredLoaded = true
	}
	var saveState *DeliveredState
	store := t.store
	if win.deliveredDirty && store != nil && win.delivered.IntentID != "" {
		copied := win.delivered
		saveState = &copied
		win.deliveredDirty = false
	}
	t.mu.Unlock()

	if saveState != nil {
		if err := store.SaveCompletionCueState(ctx, sessionID, *saveState); err != nil {
			t.mu.Lock()
			if cur := t.sessions[sessionID]; cur != nil {
				cur.deliveredDirty = true
			}
			t.mu.Unlock()
			return err
		}
	}
	return nil
}
