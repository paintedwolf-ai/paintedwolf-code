package llm

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm/compaction"
)

// ErrUtilityProviderUnconfigured marks a utility request with no configured model slot.
var ErrUtilityProviderUnconfigured = errors.New("utility model not configured")

type unconfiguredUtilityError struct {
	detail string
}

func (e *unconfiguredUtilityError) Error() string {
	if e.detail == "" {
		return ErrUtilityProviderUnconfigured.Error()
	}
	return e.detail
}

func (e *unconfiguredUtilityError) Unwrap() error { return ErrUtilityProviderUnconfigured }

func newUnconfiguredUtilityError(detail string) error {
	return &unconfiguredUtilityError{detail: detail}
}

// IsUnavailableUtilityError reports whether err reflects an unconfigured or unavailable utility slot.
func IsUnavailableUtilityError(err error) bool {
	return errors.Is(err, ErrUtilityProviderUnconfigured) ||
		errors.Is(err, ErrLiteUnavailable) ||
		errors.Is(err, compaction.ErrNoLiteProvider)
}

// UtilityBudgetExceeded reports whether the utility call's own budget expired while caller ctx stayed live.
func UtilityBudgetExceeded(err error) bool {
	var target *utilityBudgetExceededError
	return errors.As(err, &target)
}

// SummarizerFallbackError retains both stages when coordinator fallback also fails.
type SummarizerFallbackError struct {
	PrimaryProvider  string
	PrimaryModel     string
	PrimaryErr       error
	FallbackProvider string
	FallbackModel    string
	FallbackErr      error
	Selected         error
}

func (e *SummarizerFallbackError) Error() string {
	if e == nil {
		return ""
	}
	if e.Selected != nil {
		return e.Selected.Error()
	}
	return fmt.Sprintf("lite (%s/%s): %v; fallback (%s/%s): %v",
		e.PrimaryProvider, e.PrimaryModel, e.PrimaryErr,
		e.FallbackProvider, e.FallbackModel, e.FallbackErr)
}

func (e *SummarizerFallbackError) Unwrap() []error {
	if e == nil {
		return nil
	}
	if e.Selected == e.FallbackErr {
		return []error{e.FallbackErr, e.PrimaryErr}
	}
	return []error{e.PrimaryErr, e.FallbackErr}
}

func newSummarizerFallbackError(
	primaryProvider, primaryModel string, primaryErr error,
	fallbackProvider, fallbackModel string, fallbackErr error,
) *SummarizerFallbackError {
	selected := selectSummarizerCause(primaryErr, fallbackErr)
	return &SummarizerFallbackError{
		PrimaryProvider:  primaryProvider,
		PrimaryModel:     primaryModel,
		PrimaryErr:       primaryErr,
		FallbackProvider: fallbackProvider,
		FallbackModel:    fallbackModel,
		FallbackErr:      fallbackErr,
		Selected:         selected,
	}
}

func selectSummarizerCause(primaryErr, fallbackErr error) error {
	switch {
	case fallbackErr == nil:
		return primaryErr
	case primaryErr == nil:
		return fallbackErr
	case SecretScreenBlocked(fallbackErr):
		return fallbackErr
	case SecretScreenBlocked(primaryErr):
		return primaryErr
	case IsUnavailableUtilityError(fallbackErr) && !IsUnavailableUtilityError(primaryErr):
		return primaryErr
	default:
		return fallbackErr
	}
}

// preserveCallerContextError retains phase-bearing errors that already wrap ctxErr.
func preserveCallerContextError(err, ctxErr error) error {
	if ctxErr == nil {
		return err
	}
	if err == nil {
		return ctxErr
	}
	if errors.Is(err, ctxErr) || (errors.Is(ctxErr, context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded)) ||
		(errors.Is(ctxErr, context.Canceled) && errors.Is(err, context.Canceled)) {
		return err
	}
	return errors.Join(err, ctxErr)
}
