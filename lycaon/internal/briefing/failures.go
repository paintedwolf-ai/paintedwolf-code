package briefing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// FailureStage names the host step where a briefing section or attempt failed.
type FailureStage string

const (
	StageQueue                  FailureStage = "queue"
	StageAdmission              FailureStage = "admission"
	StageEvidenceReconciliation FailureStage = "evidence_reconciliation"
	StageScreening              FailureStage = "screening"
	StageGeneration             FailureStage = "generation"
	StageParse                  FailureStage = "parse"
)

// FailureKind classifies why a briefing section did not produce text.
type FailureKind string

const (
	FailureUnavailableGenerator FailureKind = "unavailable_generator"
	FailureSlotBusy             FailureKind = "slot_busy"
	FailureQueueExpired         FailureKind = "queue_expired"
	FailureScreeningFailed      FailureKind = "screening_failed"
	FailureDeadlineExceeded     FailureKind = "deadline_exceeded"
	FailureCanceled             FailureKind = "canceled"
	FailureProviderFailed       FailureKind = "provider_failed"
	FailureEmptyOutput          FailureKind = "empty_output"
	FailureMalformedOutput      FailureKind = "malformed_output"
)

// SectionFailureError pairs one section's typed failure with a safe bounded summary.
type SectionFailureError struct {
	SectionID string
	Stage     FailureStage
	Kind      FailureKind
	Summary   string
	Cause     error
}

func (e *SectionFailureError) Error() string {
	if e == nil {
		return ""
	}
	return e.PublicSummary()
}

func (e *SectionFailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// PublicSummary renders a safe bounded summary containing section ID, stage, and reason.
func (e *SectionFailureError) PublicSummary() string {
	if e == nil {
		return ""
	}
	section := strings.TrimSpace(e.SectionID)
	stage := strings.TrimSpace(string(e.Stage))
	summary := strings.TrimSpace(e.Summary)
	if summary == "" {
		summary = "generation failed"
	}
	switch {
	case section != "" && stage != "":
		return fmt.Sprintf("%s [%s]: %s", section, stage, summary)
	case section != "":
		return fmt.Sprintf("%s: %s", section, summary)
	case stage != "":
		return fmt.Sprintf("[%s]: %s", stage, summary)
	default:
		return summary
	}
}

func classifyParseError(sectionID string, parseErr error) *SectionFailureError {
	if errors.Is(parseErr, ErrNoText) {
		return &SectionFailureError{
			SectionID: sectionID,
			Stage:     StageParse,
			Kind:      FailureEmptyOutput,
			Summary:   ErrNoText.Error(),
			Cause:     parseErr,
		}
	}
	return &SectionFailureError{
		SectionID: sectionID,
		Stage:     StageParse,
		Kind:      FailureMalformedOutput,
		Summary:   "briefing output did not match section contract",
		Cause:     parseErr,
	}
}

func classifyGenerationError(sectionID string, genErr, callerErr error) *SectionFailureError {
	if genErr == nil {
		genErr = callerErr
	}
	cause := genErr
	var fallbackErr *llm.SummarizerFallbackError
	if errors.As(genErr, &fallbackErr) && fallbackErr.Selected != nil {
		cause = fallbackErr.Selected
	}

	var screenFault *llm.ModelRequestSecretScreenFaultError
	if errors.As(cause, &screenFault) {
		if screenFault.Stage == secretmatch.FaultStageManagedSecretStore {
			if errors.Is(screenFault.Err, context.DeadlineExceeded) || errors.Is(callerErr, context.DeadlineExceeded) {
				return &SectionFailureError{
					SectionID: sectionID,
					Stage:     StageEvidenceReconciliation,
					Kind:      FailureDeadlineExceeded,
					Summary:   "credential evidence check timed out",
					Cause:     genErr,
				}
			}
			if errors.Is(screenFault.Err, context.Canceled) || errors.Is(callerErr, context.Canceled) {
				return &SectionFailureError{
					SectionID: sectionID,
					Stage:     StageEvidenceReconciliation,
					Kind:      FailureCanceled,
					Summary:   "credential evidence check canceled",
					Cause:     genErr,
				}
			}
		}
		return &SectionFailureError{
			SectionID: sectionID,
			Stage:     StageScreening,
			Kind:      FailureScreeningFailed,
			Summary:   "protected content screen failed",
			Cause:     genErr,
		}
	}

	if llm.SecretScreenBlocked(cause) {
		summary := "protected content screen failed"
		switch {
		case errors.Is(cause, llm.ErrModelRequestSecretWithheld):
			summary = "protected content withheld"
		case errors.Is(cause, llm.ErrModelRequestSecretUnanswered):
			summary = "protected content approval timed out"
		}
		return &SectionFailureError{
			SectionID: sectionID,
			Stage:     StageScreening,
			Kind:      FailureScreeningFailed,
			Summary:   summary,
			Cause:     genErr,
		}
	}

	if errors.Is(cause, llm.ErrLiteBusy) {
		return &SectionFailureError{
			SectionID: sectionID,
			Stage:     StageAdmission,
			Kind:      FailureSlotBusy,
			Summary:   "admission failed (utility slot busy)",
			Cause:     genErr,
		}
	}

	if llm.IsUnavailableUtilityError(cause) {
		return &SectionFailureError{
			SectionID: sectionID,
			Stage:     StageAdmission,
			Kind:      FailureUnavailableGenerator,
			Summary:   "admission failed (utility model unavailable)",
			Cause:     genErr,
		}
	}

	if errors.Is(cause, context.DeadlineExceeded) || llm.UtilityBudgetExceeded(cause) || errors.Is(callerErr, context.DeadlineExceeded) {
		return &SectionFailureError{
			SectionID: sectionID,
			Stage:     StageGeneration,
			Kind:      FailureDeadlineExceeded,
			Summary:   "generation deadline exceeded",
			Cause:     genErr,
		}
	}

	if errors.Is(cause, context.Canceled) || errors.Is(callerErr, context.Canceled) {
		return &SectionFailureError{
			SectionID: sectionID,
			Stage:     StageGeneration,
			Kind:      FailureCanceled,
			Summary:   "generation canceled",
			Cause:     genErr,
		}
	}

	return &SectionFailureError{
		SectionID: sectionID,
		Stage:     StageGeneration,
		Kind:      FailureProviderFailed,
		Summary:   "provider request failed",
		Cause:     genErr,
	}
}

func formatSectionFailures(failures []*SectionFailureError) string {
	parts := make([]string, 0, len(failures))
	for _, f := range failures {
		if f != nil {
			parts = append(parts, f.PublicSummary())
		}
	}
	return strings.Join(parts, "; ")
}

func formatAttemptStageFailure(r resolved, stage FailureStage, kind FailureKind, summary string, cause error) string {
	if len(r.missing) == 0 {
		return (&SectionFailureError{Stage: stage, Kind: kind, Summary: summary, Cause: cause}).PublicSummary()
	}
	items := make([]*SectionFailureError, 0, len(r.missing))
	for _, idx := range r.missing {
		items = append(items, &SectionFailureError{
			SectionID: r.request.Sections[idx].ID,
			Stage:     stage,
			Kind:      kind,
			Summary:   summary,
			Cause:     cause,
		})
	}
	return formatSectionFailures(items)
}
