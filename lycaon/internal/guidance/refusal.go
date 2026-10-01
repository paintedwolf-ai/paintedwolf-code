package guidance

import (
	"errors"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Refusal carries a rendered body and structured facts.
type Refusal struct {
	// Body is the rendered reject block the agent reads.
	Body string
	// Copy is frozen at the OAR occurrence and retained for repeated-failure recovery.
	Copy map[string]string
	// Facts resolve as rejected.
	Facts ToolResultFacts
	// cause preserves the error chain.
	cause error
}

// WithCause attaches the structured error this refusal was rendered from.
func (r *Refusal) WithCause(cause error) *Refusal {
	if r == nil {
		return nil
	}
	r.cause = cause
	return r
}

// Unwrap exposes the structured cause to errors.As/Is.
func (r *Refusal) Unwrap() error {
	if r == nil {
		return nil
	}
	return r.cause
}

// NewRefusal initializes rejected facts.
func NewRefusal(code, body string) *Refusal {
	body = strings.TrimSpace(body)
	code = strings.TrimSpace(code)
	if body == "" {
		body = code
	}
	return &Refusal{
		Body: body,
		Facts: ToolResultFacts{
			Outcome: api.ToolResultOutcomeRejected,
			Codes:   nonEmptyCodes(code),
		},
	}
}

// WithDetails binds typed producer facts to this refusal's primary code.
func (r *Refusal) WithDetails(details map[string]any, subject *api.FeedbackSubject) *Refusal {
	if r == nil || r.Code() == "" {
		return r
	}
	item := api.ToolFeedback{Code: r.Code(), Details: cloneDetails(details), Subject: cloneFeedbackSubject(subject)}
	if len(r.Facts.Feedback) == 0 {
		r.Facts.Feedback = []api.ToolFeedback{item}
		return r
	}
	r.Facts.Feedback[0] = item
	return r
}

func (r *Refusal) Error() string {
	if r == nil {
		return ""
	}
	return r.Body
}

// Code is the code this refusal raised, or empty when the refusal carried none.
func (r *Refusal) Code() string {
	if r == nil {
		return ""
	}
	return r.Facts.PrimaryCode()
}

// RefusalFromError finds a refusal in an error chain.
func RefusalFromError(err error) (*Refusal, bool) {
	var reject *Refusal
	if errors.As(err, &reject) && reject != nil {
		return reject, true
	}
	return nil, false
}

func nonEmptyCodes(code string) []string {
	if code == "" {
		return nil
	}
	return []string{code}
}

// WithPolicyCopy preserves the evaluated recovery, including an empty copy.
func (r *Refusal) WithPolicyCopy(copy map[string]string) *Refusal {
	if r != nil {
		r.Copy = copy
	}
	return r
}
