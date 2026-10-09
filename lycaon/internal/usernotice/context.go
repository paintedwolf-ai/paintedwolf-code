package usernotice

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// hostFaultNotice matches host-fault prompt errors without importing their package.
type hostFaultNotice interface {
	error
	NoticeHostFault() (tool string, callRan bool)
}

// proseTurnToolCallNotice matches final-turn tool-call errors without importing their package.
type proseTurnToolCallNotice interface {
	error
	NoticeProseTurnToolCalls() (tools []string, closeoutReason string)
}

// spendCeilingNotice matches spend-ceiling prompt errors without importing their package.
type spendCeilingNotice interface {
	error
	NoticeCeilingUSD() float64
	NoticeSpentUSD() float64
	NoticeEstimateCoverage() wire.CostEstimateCoverage
	NoticeUnpricedTokens() int
	NoticeUnknownChargedCalls() int
}

// Max prompt-failure detail length.
const maxPromptFailureDetailRunes = 240

// ContextFromPromptError extracts structured template vars from typed prompt failures.
func ContextFromPromptError(err error) map[string]any {
	if err == nil {
		return nil
	}
	var thinking *llm.ThinkingOverrideError
	if errors.As(err, &thinking) {
		out := providerModelContext(thinking.ProviderID, thinking.Model)
		out["reason"] = trimContextString(thinking.Reason)
		return out
	}
	var proseTurn proseTurnToolCallNotice
	if errors.As(err, &proseTurn) {
		tools, reason := proseTurn.NoticeProseTurnToolCalls()
		out := map[string]any{}
		if len(tools) > 0 {
			out["tool"] = trimContextString(tools[0])
			out["tools"] = trimContextString(strings.Join(tools, ", "))
		}
		if reason = trimContextString(reason); reason != "" {
			out["closeout_reason"] = reason
		}
		return out
	}
	if e, ok := failure.AsProviderNotConfigured(err); ok && e != nil {
		out := map[string]any{}
		if id := trimContextString(e.ProviderID); id != "" {
			out["provider_id"] = id
		}
		return out
	}
	if e, ok := failure.AsProviderEmptyCompletion(err); ok && e != nil {
		out := map[string]any{}
		if e.OutputLimitReached() {
			out["output_limit_reached"] = true
			out["recovery_attempted"] = e.RecoveryAttempted
		}
		if id := trimContextString(e.ProviderID); id != "" {
			out["provider_id"] = id
		}
		if model := trimContextString(e.Model); model != "" {
			out["model"] = model
		}
		return out
	}
	if e, ok := failure.AsProviderContextTooSmall(err); ok && e != nil {
		out := map[string]any{}
		if id := trimContextString(e.ProviderID); id != "" {
			out["provider_id"] = id
		}
		if model := trimContextString(e.Model); model != "" {
			out["model"] = model
		}
		if e.PromptTokens > 0 {
			out["prompt_tokens"] = e.PromptTokens
		}
		if e.MaxContext > 0 {
			out["max_context"] = e.MaxContext
		}
		return out
	}
	if e, ok := failure.AsProviderToolCallsUnsupported(err); ok && e != nil {
		return providerModelContext(e.ProviderID, e.Model)
	}
	if e, ok := failure.AsProviderToolCallsInProse(err); ok && e != nil {
		return providerModelContext(e.ProviderID, e.Model)
	}
	if e, ok := failure.AsProviderResponseInterrupted(err); ok && e != nil {
		return providerModelContext(e.ProviderID, e.Model)
	}
	if e, ok := failure.AsProviderRateLimited(err); ok && e != nil {
		return providerHTTPContext(e.ProviderID, e.Model, e.Status, e.Attempts)
	}
	if e, ok := failure.AsProviderOverloaded(err); ok && e != nil {
		return providerHTTPContext(e.ProviderID, e.Model, e.Status, e.Attempts)
	}
	if e, ok := failure.AsProviderServer(err); ok && e != nil {
		return providerHTTPContext(e.ProviderID, e.Model, e.Status, e.Attempts)
	}
	if e, ok := providerretry.AsProviderRequestRejected(err); ok && e != nil {
		out := providerHTTPContext(e.ProviderID, e.Model, e.Status, 0)
		if e.Reason != "" {
			out["reason"] = string(e.Reason)
		}
		return out
	}
	if e, ok := failure.AsProviderSilent(err); ok && e != nil {
		out := providerModelContext(e.ProviderID, e.Model)
		if e.Attempts > 0 {
			out["attempts"] = e.Attempts
		}
		// Notices report whole seconds.
		if secs := int(e.Silence.Round(time.Second).Seconds()); secs > 0 {
			out["silence_seconds"] = secs
		}
		return out
	}
	if e, ok := providerretry.AsModelRefused(err); ok && e != nil {
		out := providerModelContext(e.ProviderID, e.Model)
		if code := trimContextString(e.Code); code != "" {
			out["code"] = code
		}
		return out
	}
	if e, ok := failure.AsProviderUnreachable(err); ok && e != nil {
		out := providerModelContext(e.ProviderID, e.Model)
		if e.Attempts > 0 {
			out["attempts"] = e.Attempts
		}
		return out
	}
	if nr, ok := runstate.IsNotRunnable(err); ok && nr != nil {
		out := map[string]any{}
		if reason := trimContextString(nr.Reason); reason != "" {
			out["reason"] = reason
		}
		if status := trimContextString(string(nr.Status)); status != "" {
			out["status"] = status
		}
		if runID := trimContextString(nr.RunID); runID != "" {
			out["run_id"] = runID
		}
		return out
	}
	var unavailable *workflow.WorkflowVersionUnavailableError
	if errors.As(err, &unavailable) && unavailable != nil {
		out := map[string]any{}
		if id := trimContextString(unavailable.WorkflowID); id != "" {
			out["workflow_id"] = id
		}
		if version := trimContextString(unavailable.Version); version != "" {
			out["version"] = version
		}
		return out
	}
	var fault hostFaultNotice
	if errors.As(err, &fault) && fault != nil {
		out := map[string]any{}
		tool, callRan := fault.NoticeHostFault()
		if tool = trimContextString(tool); tool != "" {
			out["tool"] = tool
		}
		if callRan {
			out["call_ran"] = true
		}
		return out
	}
	var ceiling spendCeilingNotice
	if errors.As(err, &ceiling) && ceiling != nil {
		return SpendCeilingContext(err)
	}
	// These notices need no error detail.
	switch code, _ := noticeerr.CodeOf(err); code {
	case wire.NoticeCodeSessionSpendCeilingReached,
		wire.NoticeCodeGroundingEscalated,
		wire.NoticeCodeWorkflowActive:
		return map[string]any{}
	default:
	}
	out := map[string]any{}
	if detail := sanitizePromptFailureDetail(err.Error()); detail != "" {
		out["detail"] = detail
	}
	return out
}

// SpendCeilingContext is the session_spend_ceiling_reached render context an
// error carries: the ceiling, the spend, and how complete the estimate is.
func SpendCeilingContext(err error) map[string]any {
	out := map[string]any{}
	var ceiling spendCeilingNotice
	if !errors.As(err, &ceiling) || ceiling == nil {
		return out
	}
	if ceiling.NoticeCeilingUSD() > 0 {
		out["ceiling_usd"] = formatUSD(ceiling.NoticeCeilingUSD())
	}
	if ceiling.NoticeSpentUSD() > 0 {
		out["spent_usd"] = formatUSD(ceiling.NoticeSpentUSD())
	}
	if coverage := ceiling.NoticeEstimateCoverage(); coverage != "" {
		out["estimate_coverage"] = string(coverage)
	}
	if n := ceiling.NoticeUnpricedTokens(); n > 0 {
		out["unpriced_tokens"] = n
	}
	if n := ceiling.NoticeUnknownChargedCalls(); n > 0 {
		out["unknown_charged_calls"] = n
	}
	return out
}

func trimContextString(s string) string {
	return strings.TrimSpace(s)
}

func formatUSD(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

func providerModelContext(providerID, model string) map[string]any {
	out := map[string]any{}
	if id := trimContextString(providerID); id != "" {
		out["provider_id"] = id
	}
	if m := trimContextString(model); m != "" {
		out["model"] = m
	}
	return out
}

func providerHTTPContext(providerID, model string, status, attempts int) map[string]any {
	out := providerModelContext(providerID, model)
	if status > 0 {
		out["status"] = status
	}
	if attempts > 0 {
		out["attempts"] = attempts
	}
	return out
}

// sanitizePromptFailureDetail prepares a bounded redacted cause.
func sanitizePromptFailureDetail(raw string) string {
	s := observability.RedactString(raw)
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxPromptFailureDetailRunes {
		return s
	}
	return runeclamp.Fit(s, maxPromptFailureDetailRunes)
}
