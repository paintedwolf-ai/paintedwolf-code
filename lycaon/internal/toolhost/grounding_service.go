package toolhost

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
)

// GuidanceNudger queues structured coordinator guidance after non-blocking grounding warnings.
type GuidanceNudger interface {
	QueueNudge(ctx context.Context, sessionID, code string, data map[string]any)
}

// GroundingService audits record_finding findings against the evidence ledger.
type GroundingService struct {
	Config    delegation.GroundingConfig
	State     *grounding.StateStore
	Ledger    guidance.EvidenceLedgerReader
	RejectFmt *guidance.StaticRejectFormatter
	Nudger    GuidanceNudger
}

// AuditFinding grounds a record_finding finding (summary + optional ref) against the
// evidence ledger before it lands in the findings store. Returns a structured reject
// in block mode / on escalation; in warn mode it queues a guidance nudge and allows
// the finding.
func (s *GroundingService) AuditFinding(ctx context.Context, summary, ref string, tctx tools.ToolContext) error {
	if s == nil {
		return nil
	}
	mode := s.Config.WriteGroundingMode()
	if mode == "off" {
		return nil
	}
	sessionID := strings.TrimSpace(tctx.Identity.SessionID)
	if sessionID == "" {
		return nil
	}
	if s.State != nil && s.State.IsEscalated(sessionID) && s.Config.CircuitBreaker.EscalateMode == "block" {
		return s.formatReject(guidance.FindingUngroundedCode, map[string]any{"reason": "grounding_escalated"})
	}
	if s.Ledger == nil {
		return nil
	}
	ev, err := s.Ledger.LoadLedger(ctx, sessionID)
	if err != nil {
		return err
	}
	eval := guidance.EvaluateFindingSummary(tctx.ActiveRootPath(), summary, ref, ev)
	if eval.Grounded {
		if s.State != nil {
			state := s.State.Get(sessionID)
			grounding.ResetUngroundedStreak(&state)
			s.State.Set(sessionID, state)
		}
		return nil
	}
	if s.State != nil {
		state := s.State.Get(sessionID)
		grounding.ApplyUngroundedWarning(
			&state,
			s.Config.CircuitBreaker.MaxUngroundedWarnings,
			s.Config.CircuitBreaker.MaxConsecutiveUngrounded,
			s.Config.CircuitBreaker.EscalateMode == "block",
		)
		s.State.Set(sessionID, state)
		if state.Escalated || mode == "block" {
			return s.formatReject(eval.Code, guidance.GroundingHintData(eval.Offenders, ev))
		}
	} else if mode == "block" {
		return s.formatReject(eval.Code, guidance.GroundingHintData(eval.Offenders, ev))
	}
	if s.Nudger != nil {
		s.Nudger.QueueNudge(ctx, sessionID, eval.Code, guidance.OffenderHintData(eval.Offenders))
	}
	return nil
}

func (s *GroundingService) formatReject(code string, data map[string]any) error {
	return &toolrejection.ToolReject{Code: code, Data: data}
}
