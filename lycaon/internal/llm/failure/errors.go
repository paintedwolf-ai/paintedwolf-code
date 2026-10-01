// Package failure defines typed provider failures shared by dispatch, transport, and compaction.
package failure

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// ErrProviderNotConfigured marks a provider without usable credentials.
var ErrProviderNotConfigured = errors.New("provider not configured")

// ProviderNotConfiguredError identifies the unconfigured provider for HTTP mapping.
type ProviderNotConfiguredError struct {
	ProviderID string
}

func (e *ProviderNotConfiguredError) Error() string {
	if e == nil || e.ProviderID == "" {
		return ErrProviderNotConfigured.Error()
	}
	return fmt.Sprintf("provider %q is not configured", e.ProviderID)
}

func (e *ProviderNotConfiguredError) Is(target error) bool {
	return target == ErrProviderNotConfigured
}

// AsProviderNotConfigured reports whether err is a provider-not-configured failure.
func AsProviderNotConfigured(err error) (*ProviderNotConfiguredError, bool) {
	var e *ProviderNotConfiguredError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderEmptyCompletion is returned when the model returns no text or tool calls.
var ErrProviderEmptyCompletion = errors.New("provider returned no completion content")

// ProviderEmptyCompletionError identifies provider/model for HTTP mapping.
type ProviderEmptyCompletionError struct {
	ProviderID string
	Model      string
	// Retryable permits deterministic request replay.
	Retryable bool
	// Terminal records an explicit upstream completion terminator.
	Terminal bool
	// Reason preserves a provider's structured terminal reason when present.
	Reason            string
	Attempts          int
	RecoveryAttempted bool
}

// OutputLimitReached recognizes terminal tokens from the supported protocols.
func (e *ProviderEmptyCompletionError) OutputLimitReached() bool {
	if e == nil || !e.Terminal {
		return false
	}
	switch e.Reason {
	case "length", "max_tokens", "MAX_TOKENS":
		return true
	default:
		return false
	}
}

func (e *ProviderEmptyCompletionError) Error() string {
	if e == nil {
		return ErrProviderEmptyCompletion.Error()
	}
	label := "provider"
	if identity := strings.Trim(strings.TrimSpace(e.ProviderID)+"/"+strings.TrimSpace(e.Model), "/"); identity != "" {
		label += " " + identity
	}
	detail := ""
	if strings.TrimSpace(e.Reason) != "" {
		detail = fmt.Sprintf(" (%s)", strings.TrimSpace(e.Reason))
	}
	if e.Attempts > 1 {
		return fmt.Sprintf("%s returned no content after %d attempts%s", label, e.Attempts, detail)
	}
	return fmt.Sprintf("%s returned no content%s", label, detail)
}

func (e *ProviderEmptyCompletionError) Is(target error) bool {
	return target == ErrProviderEmptyCompletion
}

// AsProviderEmptyCompletion reports whether err is an empty completion from the provider.
func AsProviderEmptyCompletion(err error) (*ProviderEmptyCompletionError, bool) {
	var e *ProviderEmptyCompletionError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderOutputTruncated marks output-budget exhaustion.
var ErrProviderOutputTruncated = errors.New("provider output truncated")

// ProviderOutputTruncatedError carries the reported stop reason and token counts.
type ProviderOutputTruncatedError struct {
	ProviderID       string
	Model            string
	FinishReason     string
	PromptTokens     int
	CompletionTokens int
	ContextTokens    int
}

func (e *ProviderOutputTruncatedError) Error() string {
	if e == nil {
		return ErrProviderOutputTruncated.Error()
	}
	return fmt.Sprintf("provider %s/%s output truncated (%s; prompt=%d completion=%d context=%d)",
		e.ProviderID, e.Model, e.FinishReason, e.PromptTokens, e.CompletionTokens, e.ContextTokens)
}

func (e *ProviderOutputTruncatedError) Is(target error) bool {
	return target == ErrProviderOutputTruncated
}

// AsProviderOutputTruncated reports whether err is a provider output limit.
func AsProviderOutputTruncated(err error) (*ProviderOutputTruncatedError, bool) {
	var e *ProviderOutputTruncatedError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderContextTooSmall reports an unfittable prompt and output reserve.
var ErrProviderContextTooSmall = errors.New("provider model context too small for prompt")

// ProviderContextTooSmallError carries the prompt size and context ceiling.
type ProviderContextTooSmallError struct {
	ProviderID        string
	Model             string
	PromptTokens      int
	CompletionReserve int
	MaxContext        int
}

func (e *ProviderContextTooSmallError) Error() string {
	if e == nil {
		return ErrProviderContextTooSmall.Error()
	}
	if e.CompletionReserve > 0 {
		return fmt.Sprintf("provider %s/%s: prompt ~%d tokens plus %d output tokens exceeds model context %d",
			e.ProviderID, e.Model, e.PromptTokens, e.CompletionReserve, e.MaxContext)
	}
	return fmt.Sprintf("provider %s/%s: prompt ~%d tokens exceeds model context %d",
		e.ProviderID, e.Model, e.PromptTokens, e.MaxContext)
}

func (e *ProviderContextTooSmallError) Is(target error) bool {
	return target == ErrProviderContextTooSmall
}

// AsProviderContextTooSmall reports whether err is a context-window overflow.
func AsProviderContextTooSmall(err error) (*ProviderContextTooSmallError, bool) {
	var e *ProviderContextTooSmallError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderToolCallsUnsupported marks unavailable structured tool calling.
var ErrProviderToolCallsUnsupported = errors.New("provider model does not support tool calling")

// ProviderToolCallsUnsupportedError identifies the incompatible model.
type ProviderToolCallsUnsupportedError struct {
	ProviderID string
	Model      string
}

func (e *ProviderToolCallsUnsupportedError) Error() string {
	if e == nil {
		return ErrProviderToolCallsUnsupported.Error()
	}
	if e.ProviderID != "" && e.Model != "" {
		return fmt.Sprintf("provider %s/%s does not support tool calling", e.ProviderID, e.Model)
	}
	return ErrProviderToolCallsUnsupported.Error()
}

func (e *ProviderToolCallsUnsupportedError) Is(target error) bool {
	return target == ErrProviderToolCallsUnsupported
}

// AsProviderToolCallsUnsupported reports whether err is a tool-calling-unsupported failure.
func AsProviderToolCallsUnsupported(err error) (*ProviderToolCallsUnsupportedError, bool) {
	var e *ProviderToolCallsUnsupportedError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderToolCallsInProse marks an unrecoverable textual tool call.
var ErrProviderToolCallsInProse = errors.New("provider model emitted an unparseable tool call in prose")

// ProviderToolCallsInProseError identifies the malformed response source.
type ProviderToolCallsInProseError struct {
	ProviderID string
	Model      string
}

func (e *ProviderToolCallsInProseError) Error() string {
	if e == nil {
		return ErrProviderToolCallsInProse.Error()
	}
	if e.ProviderID != "" && e.Model != "" {
		return fmt.Sprintf("provider %s/%s emitted an unparseable tool call in prose", e.ProviderID, e.Model)
	}
	return ErrProviderToolCallsInProse.Error()
}

func (e *ProviderToolCallsInProseError) Is(target error) bool {
	return target == ErrProviderToolCallsInProse
}

// AsProviderToolCallsInProse reports whether err is an unparseable in-prose tool call.
func AsProviderToolCallsInProse(err error) (*ProviderToolCallsInProseError, bool) {
	var e *ProviderToolCallsInProseError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderRateLimited is returned when HTTP 429 retries are exhausted.
var ErrProviderRateLimited = errors.New("provider rate limited")

// ProviderRateLimitedError identifies the provider/model and HTTP status for the notice rail.
type ProviderRateLimitedError struct {
	ProviderID string
	Model      string
	Status     int
	// Attempts counts every request, including the first.
	Attempts int
	Detail   string
}

func (e *ProviderRateLimitedError) Error() string {
	if e == nil {
		return ErrProviderRateLimited.Error()
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.ProviderID != "" && e.Status > 0 {
		return fmt.Sprintf("provider %s rate limited (HTTP %d)", e.ProviderID, e.Status)
	}
	return ErrProviderRateLimited.Error()
}

func (e *ProviderRateLimitedError) Is(target error) bool {
	return target == ErrProviderRateLimited
}

// AsProviderRateLimited reports whether err is provider rate-limit exhaustion.
func AsProviderRateLimited(err error) (*ProviderRateLimitedError, bool) {
	var e *ProviderRateLimitedError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderOverloaded is returned when HTTP 503/529 retries are exhausted.
var ErrProviderOverloaded = errors.New("provider overloaded")

// ProviderOverloadedError identifies the provider/model and HTTP status for the notice rail.
type ProviderOverloadedError struct {
	ProviderID string
	Model      string
	Status     int
	// Attempts counts every request, including the first.
	Attempts int
	Detail   string
}

func (e *ProviderOverloadedError) Error() string {
	if e == nil {
		return ErrProviderOverloaded.Error()
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.ProviderID != "" && e.Status > 0 {
		return fmt.Sprintf("provider %s overloaded (HTTP %d)", e.ProviderID, e.Status)
	}
	return ErrProviderOverloaded.Error()
}

func (e *ProviderOverloadedError) Is(target error) bool {
	return target == ErrProviderOverloaded
}

// AsProviderOverloaded reports whether err is provider capacity exhaustion.
func AsProviderOverloaded(err error) (*ProviderOverloadedError, bool) {
	var e *ProviderOverloadedError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderServer marks exhausted non-capacity HTTP 5xx responses.
var ErrProviderServer = errors.New("provider server error")

// ProviderServerError keeps diagnostics separate from user-facing facts.
type ProviderServerError struct {
	ProviderID string
	Model      string
	Status     int
	// Attempts counts every request, including the first.
	Attempts int
	Detail   string
}

func (e *ProviderServerError) Error() string {
	if e == nil {
		return ErrProviderServer.Error()
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.ProviderID != "" && e.Status > 0 {
		return fmt.Sprintf("provider %s server error (HTTP %d)", e.ProviderID, e.Status)
	}
	return ErrProviderServer.Error()
}

func (e *ProviderServerError) Is(target error) bool {
	return target == ErrProviderServer
}

// AsProviderServer reports whether err is an exhausted provider-side 5xx.
func AsProviderServer(err error) (*ProviderServerError, bool) {
	var e *ProviderServerError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderUnreachable marks spent retries that never reached the provider.
var ErrProviderUnreachable = errors.New("provider unreachable")

// ProviderUnreachableError identifies a provider no request could reach.
type ProviderUnreachableError struct {
	ProviderID string
	Model      string
	// Attempts counts every try, including the first.
	Attempts int
	Cause    error
}

func (e *ProviderUnreachableError) Error() string {
	if e == nil {
		return ErrProviderUnreachable.Error()
	}
	if e.ProviderID != "" && e.Cause != nil {
		return fmt.Sprintf("provider %s unreachable after %d attempts: %v", e.ProviderID, e.Attempts, e.Cause)
	}
	return ErrProviderUnreachable.Error()
}

func (e *ProviderUnreachableError) Is(target error) bool {
	return target == ErrProviderUnreachable
}

func (e *ProviderUnreachableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// AsProviderUnreachable reports an undelivered request.
func AsProviderUnreachable(err error) (*ProviderUnreachableError, bool) {
	var e *ProviderUnreachableError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ErrProviderSilent marks delivered requests with no response headers.
var ErrProviderSilent = errors.New("provider accepted the request and did not answer")

// ProviderSilentError identifies delivered work with no response headers.
type ProviderSilentError struct {
	ProviderID string
	Model      string
	// Attempts counts every try, including the first.
	Attempts int
	// Silence is the longest single stretch a request went unanswered.
	Silence time.Duration
	Cause   error
}

func (e *ProviderSilentError) Error() string {
	if e == nil {
		return ErrProviderSilent.Error()
	}
	if e.ProviderID != "" {
		return fmt.Sprintf("provider %s accepted %d request(s) and sent no response headers within %s",
			e.ProviderID, e.Attempts, e.Silence.Round(time.Second))
	}
	return ErrProviderSilent.Error()
}

func (e *ProviderSilentError) Is(target error) bool {
	return target == ErrProviderSilent
}

func (e *ProviderSilentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// AsProviderSilent reports a silent delivered request.
func AsProviderSilent(err error) (*ProviderSilentError, bool) {
	var e *ProviderSilentError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Notice codes for provider failures the host surfaces to the user.
func (e *ProviderNotConfiguredError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderNotConfigured
}

func (e *ProviderEmptyCompletionError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderEmptyCompletion
}

func (e *ProviderContextTooSmallError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderContextTooSmall
}

func (e *ProviderToolCallsUnsupportedError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderToolCallsUnsupported
}

func (e *ProviderToolCallsInProseError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderToolCallsInProse
}

func (e *ProviderRateLimitedError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderRateLimited
}

func (e *ProviderOverloadedError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderOverloaded
}

func (e *ProviderServerError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderServerError
}

func (e *ProviderUnreachableError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderUnreachable
}

func (e *ProviderSilentError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderSilent
}

// ProviderResponseInterruptedError preserves a failed response after headers arrived.
type ProviderResponseInterruptedError struct {
	ProviderID string
	Model      string
	Cause      error
}

func (e *ProviderResponseInterruptedError) Error() string {
	return fmt.Sprintf("provider %s/%s response interrupted: %v", e.ProviderID, e.Model, e.Cause)
}

func (e *ProviderResponseInterruptedError) Unwrap() error { return e.Cause }

func (e *ProviderResponseInterruptedError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderResponseInterrupted
}

func AsProviderResponseInterrupted(err error) (*ProviderResponseInterruptedError, bool) {
	var response *ProviderResponseInterruptedError
	ok := errors.As(err, &response)
	return response, ok
}

func InterruptedResponse(ctx context.Context, providerID, model string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &ProviderResponseInterruptedError{ProviderID: providerID, Model: model, Cause: err}
}
