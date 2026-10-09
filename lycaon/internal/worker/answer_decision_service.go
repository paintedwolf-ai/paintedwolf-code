package worker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	DecisionJobIDMismatchCode   = "DECISION_JOB_ID_MISMATCH"
	DecisionNotFoundCode        = "DECISION_NOT_FOUND"
	DecisionSessionMismatchCode = "DECISION_SESSION_MISMATCH"
)

// AnswerDecisionService resolves worker decisions.
type AnswerDecisionService struct {
	Queue     WorkerQueue
	Decisions sessiondecisions.Store
	Resolver  DecisionResolver
	Reject    *guidance.StaticRejectFormatter
}

// AnswerDecisionResult identifies the resolved choice.
type AnswerDecisionResult struct {
	JobID      string
	Option     string
	ResolvedBy string
}

// AnswerJob resolves a pending worker decision.
func (s *AnswerDecisionService) AnswerJob(ctx context.Context, sessionID, jobID, option, resolvedBy string) (AnswerDecisionResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	option = strings.TrimSpace(option)
	resolvedBy = strings.TrimSpace(resolvedBy)
	if resolvedBy == "" {
		resolvedBy = "coordinator"
	}
	switch resolvedBy {
	case "coordinator", "user":
	default:
		return AnswerDecisionResult{}, fmt.Errorf("resolved_by must be coordinator or user")
	}
	if s == nil || s.Queue == nil || s.Decisions == nil || s.Resolver == nil {
		return AnswerDecisionResult{}, fmt.Errorf("answer decision not configured")
	}
	if jobID == "" {
		return AnswerDecisionResult{}, fmt.Errorf("job_id is required")
	}
	if option == "" {
		return AnswerDecisionResult{}, fmt.Errorf("option is required")
	}
	task, ok := s.Queue.Get(jobID)
	if !ok || task == nil {
		return AnswerDecisionResult{}, s.reject(DecisionNotFoundCode, map[string]any{"job_id": jobID})
	}
	if strings.TrimSpace(task.ParentSessionID) != sessionID {
		return AnswerDecisionResult{}, s.reject(DecisionSessionMismatchCode, map[string]any{"job_id": jobID})
	}
	child := strings.TrimSpace(task.ChildSessionID)
	if child == "" {
		return AnswerDecisionResult{}, fmt.Errorf("job %q has no worker session to resume", jobID)
	}
	dec, ok, err := s.Decisions.Get(ctx, child)
	if err != nil {
		return AnswerDecisionResult{}, fmt.Errorf("load decision: %w", err)
	}
	if !ok {
		return AnswerDecisionResult{}, s.reject(DecisionNotFoundCode, map[string]any{"job_id": jobID})
	}
	if dec.WorkerID != jobID {
		return AnswerDecisionResult{}, s.reject(DecisionJobIDMismatchCode, map[string]any{
			"job_id":          jobID,
			"expected_job_id": dec.WorkerID,
		})
	}
	chosen, ok := resolveDecisionOption(option, dec.Options)
	if !ok {
		return AnswerDecisionResult{}, fmt.Errorf("option %q is not one of the offered options: %s", option, strings.Join(dec.Options, " | "))
	}
	err = s.Resolver.Resolve(ctx, dec, DecisionAnswerMessage(chosen, resolvedBy))
	if err != nil {
		if errors.Is(err, errDecisionMissing) {
			return AnswerDecisionResult{}, s.reject(DecisionNotFoundCode, map[string]any{"job_id": jobID})
		}
		if errors.Is(err, errDecisionJobMismatch) {
			return AnswerDecisionResult{}, s.reject(DecisionJobIDMismatchCode, map[string]any{"job_id": jobID})
		}
		if errors.Is(err, errDecisionChanged) {
			return AnswerDecisionResult{}, s.reject("DECISION_CHANGED", map[string]any{"job_id": jobID})
		}
		var stateErr *DecisionStateError
		if errors.As(err, &stateErr) {
			return AnswerDecisionResult{}, s.reject("DECISION_NOT_SUSPENDED", map[string]any{"job_id": jobID, "status": stateErr.Status})
		}
		return AnswerDecisionResult{}, err
	}
	return AnswerDecisionResult{JobID: jobID, Option: chosen, ResolvedBy: resolvedBy}, nil
}

func (s *AnswerDecisionService) reject(code string, data map[string]any) error {
	var formatter *guidance.StaticRejectFormatter
	if s != nil {
		formatter = s.Reject
	}
	return tools.FormatDecisionReject(code, data, formatter)
}

// resolveDecisionOption accepts an option's text or a one-based index. Text wins,
// so a numeric option is not read as an index.
func resolveDecisionOption(input string, options []string) (string, bool) {
	input = strings.TrimSpace(input)
	for _, o := range options {
		if strings.EqualFold(strings.TrimSpace(o), input) {
			return o, true
		}
	}
	if n, err := strconv.Atoi(input); err == nil && n >= 1 && n <= len(options) {
		return options[n-1], true
	}
	return "", false
}

// These prefixes identify host-authored decision answers.
const (
	DecisionAnswerCoordinator = "Coordinator decision"
	DecisionAnswerUser        = "User decision (relayed by coordinator)"
	decisionAnswerContinue    = "Continue the task with this choice."
)

// DecisionAnswerMessage renders the resume message for a resolved decision.
func DecisionAnswerMessage(chosen, resolvedBy string) api.Message {
	provenance := DecisionAnswerCoordinator
	if resolvedBy == "user" {
		provenance = DecisionAnswerUser
	}
	return api.Message{
		Role:    api.MessageRoleUser,
		Content: provenance + ": " + chosen + "\n" + decisionAnswerContinue,
	}
}

// DecisionAnswerFromMessage parses the host-authored decision prefix.
func DecisionAnswerFromMessage(message api.Message) (option, resolvedBy string, ok bool) {
	if message.Role != api.MessageRoleUser {
		return "", "", false
	}
	first, _, _ := strings.Cut(message.Content, "\n")
	for prefix, by := range map[string]string{DecisionAnswerCoordinator: "coordinator", DecisionAnswerUser: "user"} {
		if rest, found := strings.CutPrefix(first, prefix+": "); found {
			return rest, by, true
		}
	}
	return "", "", false
}
