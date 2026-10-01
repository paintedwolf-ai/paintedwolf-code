package tools

import (
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

func approvalRepeatSubject(plan *hitl.ApprovalPlan) string {
	if plan == nil || len(plan.Subject.Targets) == 0 {
		return ""
	}
	return strings.TrimSpace(plan.Subject.Targets[0].Label)
}

func approvalReasonKey(decision *gate.Decision) string {
	if decision == nil {
		return ""
	}
	return strings.TrimSpace(decision.ReasonKey)
}

func (e *DefaultToolExecutor) noteRepeatAsk(chat string, in toolApprovalRaise) *hitl.RepeatContext {
	if e == nil || e.gateRepeat == nil {
		return nil
	}
	reasonKey := approvalReasonKey(in.Decision)
	if reasonKey == "" {
		return nil
	}
	snap := e.gateRepeat.NoteAsk(chat, reasonKey, approvalRepeatSubject(in.Plan))
	if snap.Count <= 1 && snap.Asks <= 1 {
		return nil
	}
	return &hitl.RepeatContext{
		ReasonKey:         snap.ReasonKey,
		Count:             snap.Count,
		Asks:              snap.Asks,
		Subjects:          append([]string(nil), snap.Subjects...),
		SubjectsTruncated: snap.SubjectsTruncated,
		SuppressedCount:   snap.Suppressed,
	}
}

func (e *DefaultToolExecutor) noteRepeatSuppressed(chat string, in toolApprovalRaise) {
	if e == nil || e.gateRepeat == nil {
		return
	}
	reasonKey := approvalReasonKey(in.Decision)
	if reasonKey == "" {
		return
	}
	e.gateRepeat.NoteSuppressed(chat, reasonKey, approvalRepeatSubject(in.Plan))
}

// quietOptionsFor mints quiet rungs with this executor's live-quiet callback.
func (e *DefaultToolExecutor) quietOptionsFor(action hitl.ProposedAction, decision *gate.Decision) []hitl.ApprovalOption {
	chat := action.ChatSession()
	return hitl.QuietOptions(action, decision, nil, func(key string) bool {
		if e == nil || e.approvalGate == nil {
			return false
		}
		_, live := e.approvalGate.AskQuietLive(chat, key)
		return live
	})
}

// decisionQuieted requires quiet coverage for every reason. Standing redaction is checked separately.
func (e *DefaultToolExecutor) decisionQuieted(chat string, in toolApprovalRaise) bool {
	if e == nil || e.approvalGate == nil || in.Decision == nil {
		return false
	}
	if in.SecretScreenHit && in.SecretScreen == nil {
		return false
	}
	if in.SecretScreen != nil && (in.SecretScreen.Contested || len(in.SecretScreen.Recipients) > 1) {
		return false
	}
	return hitl.DecisionFullyQuieted(in.Decision, in.SecretScreen, hitl.GrantKey(in.Action), func(key string) bool {
		_, ok := e.approvalGate.AskQuietLive(chat, key)
		return ok
	})
}

func (e *DefaultToolExecutor) noteQuietSuppressed(chat string, in toolApprovalRaise) {
	if e == nil || e.approvalGate == nil || in.Decision == nil {
		return
	}
	for _, subj := range hitl.QuietSubjectsFromDecision(in.Decision, in.SecretScreen, hitl.GrantKey(in.Action)) {
		e.approvalGate.NoteAskQuietSuppressed(chat, subj.Key)
	}
}

func (e *DefaultToolExecutor) quietSkipKeys(chat string, in toolApprovalRaise) []string {
	if e == nil || e.approvalGate == nil || in.Decision == nil {
		return nil
	}
	var out []string
	for _, subj := range hitl.QuietSubjectsFromDecision(in.Decision, in.SecretScreen, hitl.GrantKey(in.Action)) {
		if _, ok := e.approvalGate.AskQuietLive(chat, subj.Key); ok {
			out = append(out, subj.Key)
		}
	}
	return out
}
