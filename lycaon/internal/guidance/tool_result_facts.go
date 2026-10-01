package guidance

import (
	"reflect"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/pkg/api"
)

// ToolResultFacts records an invocation outcome and feedback.
type ToolResultFacts struct {
	// ContentReplaced prevents parallel captures from exposing content policy replaced or withheld.
	ContentReplaced bool
	// ProviderErrorCode is a structured error identifier supplied by a provider adapter.
	ProviderErrorCode string
	// The zero outcome resolves as completed.
	Outcome api.ToolResultOutcome
	// Codes retain feedback order, with a settled refusal first for card selection.
	Codes []string
	// Feedback retains distinct observations; Codes is its unique code projection.
	Feedback []api.ToolFeedback
	// Confine is the post-invoke boundary observation.
	Confine confine.Observation
	// IndexWatch is the Git index a command started with; post-invoke guidance
	// releases it.
	IndexWatch indexwatch.Snapshot
}

// Resolution returns completed for an unstated outcome.
func (f ToolResultFacts) Resolution() api.ToolResultOutcome {
	if f.Outcome == "" {
		return api.ToolResultOutcomeCompleted
	}
	return f.Outcome
}

// Succeeded reports whether this invocation resolved as a usable result.
func (f ToolResultFacts) Succeeded() bool {
	return f.Resolution() == api.ToolResultOutcomeCompleted
}

// UnstatedNonSuccess reports a non-success outcome with no stated Code.
func (f ToolResultFacts) UnstatedNonSuccess() bool {
	switch f.Resolution() {
	case api.ToolResultOutcomeError, api.ToolResultOutcomeRejected:
		return f.PrimaryCode() == ""
	default:
		return false
	}
}

// PrimaryCode drives card selection.
func (f ToolResultFacts) PrimaryCode() string {
	if len(f.Codes) == 0 {
		return ""
	}
	return f.Codes[0]
}

// HasCode reports whether code was already raised against this result.
func (f ToolResultFacts) HasCode(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	for _, existing := range f.Codes {
		if existing == code {
			return true
		}
	}
	return false
}

// WithCode adds a code once.
func (f ToolResultFacts) WithCode(code string) ToolResultFacts {
	if f.HasCode(code) {
		return f
	}
	return f.WithFeedback(code, nil, nil)
}

// WithFeedback raises one code with its details and subject.
func (f ToolResultFacts) WithFeedback(code string, details map[string]any, subject *api.FeedbackSubject) ToolResultFacts {
	code = strings.TrimSpace(code)
	if code == "" {
		return f
	}
	if len(details) == 0 {
		details = nil
	}
	for _, item := range f.Feedback {
		if item.Code == code && reflect.DeepEqual(item.Subject, subject) && reflect.DeepEqual(item.Details, details) {
			return f
		}
	}
	next := f
	if !f.HasCode(code) {
		next.Codes = append(append(make([]string, 0, len(f.Codes)+1), f.Codes...), code)
	}
	next.Feedback = append(append(make([]api.ToolFeedback, 0, len(f.Feedback)+1), f.Feedback...), api.ToolFeedback{
		Code: code, Details: cloneDetails(details), Subject: cloneFeedbackSubject(subject),
	})
	return next
}

// WithOutcome retains the most decisive outcome.
func (f ToolResultFacts) WithOutcome(outcome api.ToolResultOutcome) ToolResultFacts {
	if outcomeRank(outcome) <= outcomeRank(f.Outcome) {
		return f
	}
	next := f
	next.Outcome = outcome
	return next
}

// Merge combines outcome and feedback.
func (f ToolResultFacts) Merge(other ToolResultFacts) ToolResultFacts {
	next := f.WithOutcome(other.Outcome)
	next.ContentReplaced = f.ContentReplaced || other.ContentReplaced
	if other.ProviderErrorCode != "" {
		next.ProviderErrorCode = other.ProviderErrorCode
	}
	for _, feedback := range feedbackForFacts(other) {
		next = next.WithFeedback(feedback.Code, feedback.Details, feedback.Subject)
	}
	if other.Confine.Applied {
		next.Confine = other.Confine
	}
	if other.IndexWatch.Active() {
		next.IndexWatch = other.IndexWatch
	}
	return next
}

// FeedbackFor returns the observation for code.
func (f ToolResultFacts) FeedbackFor(code string) api.ToolFeedback {
	for _, item := range f.Feedback {
		if item.Code == code {
			return item
		}
	}
	return api.ToolFeedback{Code: code}
}

func cloneDetails(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	return jsonvalue.CloneMap(in)
}

func cloneFeedbackSubject(in *api.FeedbackSubject) *api.FeedbackSubject {
	if in == nil {
		return nil
	}
	copy := *in
	return &copy
}

// outcomeRank orders outcomes by how decisively they end an invocation.
func outcomeRank(outcome api.ToolResultOutcome) int {
	switch outcome {
	case api.ToolResultOutcomeRejected:
		return 3
	case api.ToolResultOutcomeError:
		return 2
	case api.ToolResultOutcomeCompleted:
		return 1
	default:
		return 0
	}
}
