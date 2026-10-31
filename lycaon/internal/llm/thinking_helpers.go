package llm

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
	"math"
	"slices"
	"strings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func thinkingOverridesToDTO(overrides []modelcall.ThinkingOverride) []api.ThinkingOverride {
	out := make([]api.ThinkingOverride, 0, len(overrides))
	for _, o := range cloneThinkingOverrides(overrides) {
		out = append(out, api.ThinkingOverride{ProviderID: o.ProviderID, Model: o.Model, Mode: o.Mode, Effort: o.Effort, Enabled: o.Enabled, BudgetTokens: o.BudgetTokens})
	}
	return out
}

func thinkingOverridesFromDTO(overrides []api.ThinkingOverride) []modelcall.ThinkingOverride {
	out := make([]modelcall.ThinkingOverride, 0, len(overrides))
	for _, o := range overrides {
		out = append(out, modelcall.ThinkingOverride{ProviderID: o.ProviderID, Model: o.Model, Mode: o.Mode, Effort: o.Effort, Enabled: o.Enabled, BudgetTokens: o.BudgetTokens})
	}
	return cloneThinkingOverrides(out)
}

func thinkingCapabilitiesToDTO(c modelinfo.ThinkingCapabilities) api.ThinkingCapabilities {
	out := api.ThinkingCapabilities{State: c.State, Efforts: c.Efforts, CanEnable: c.CanEnable, CanDisable: c.CanDisable, DefaultEffort: c.DefaultEffort, Source: c.Source}
	if c.Budget != nil {
		out.Budget = &api.ThinkingBudgetRange{Min: c.Budget.Min, Max: c.Budget.Max}
	}
	return out
}

func registryThinkingProfile(snapshot *providerRegistrySnapshot, id string) providerprofile.Profile {
	if snapshot != nil && snapshot.providers[id] != nil {
		return snapshot.providers[id].Profile()
	}
	return providerprofile.Default()
}

func validateThinkingOverride(o modelcall.ThinkingOverride) error {
	if strings.TrimSpace(o.ProviderID) == "" || strings.TrimSpace(o.Model) == "" {
		return fmt.Errorf("thinking override requires provider_id and model")
	}
	controls := 0
	if o.Effort != "" {
		controls++
	}
	if o.Enabled != nil {
		controls++
	}
	if o.BudgetTokens != nil {
		controls++
	}
	switch o.Mode {
	case "application":
		if controls != 0 {
			return fmt.Errorf("application thinking cannot specify a fixed control")
		}
	case "fixed":
		if controls != 1 {
			return fmt.Errorf("fixed thinking requires exactly one of effort, enabled, or budget_tokens")
		}
		if o.Effort != "" && !modelinfo.ValidReasoningToken(o.Effort) {
			return fmt.Errorf("invalid thinking effort %q", o.Effort)
		}
		if o.BudgetTokens != nil && (*o.BudgetTokens <= 0 || *o.BudgetTokens > math.MaxInt32-providerprofile.DefaultAnthropicMaxTokens) {
			return fmt.Errorf("thinking budget must be a positive supported token count; use enabled: false to disable thinking")
		}
	default:
		return fmt.Errorf("unknown thinking override mode %q", o.Mode)
	}
	return nil
}

func validateThinkingOverrides(overrides []modelcall.ThinkingOverride) error {
	seen := make(map[ModelRef]bool)
	for _, override := range overrides {
		if err := validateThinkingOverride(override); err != nil {
			return err
		}
		key := ModelRef{ProviderID: override.ProviderID, Model: override.Model}
		if seen[key] {
			return fmt.Errorf("duplicate thinking override for %s / %s", key.ProviderID, key.Model)
		}
		seen[key] = true
	}
	return nil
}

func cloneThinkingOverrides(in []modelcall.ThinkingOverride) []modelcall.ThinkingOverride {
	out := slices.Clone(in)
	for i := range out {
		if out[i].Enabled != nil {
			value := *out[i].Enabled
			out[i].Enabled = &value
		}
		if out[i].BudgetTokens != nil {
			value := *out[i].BudgetTokens
			out[i].BudgetTokens = &value
		}
	}
	return out
}

func mergeThinkingOverrides(base, overlay []modelcall.ThinkingOverride) []modelcall.ThinkingOverride {
	out := cloneThinkingOverrides(base)
	for _, entry := range cloneThinkingOverrides(overlay) {
		index := slices.IndexFunc(out, func(current modelcall.ThinkingOverride) bool {
			return current.ProviderID == entry.ProviderID && current.Model == entry.Model
		})
		if index >= 0 {
			out[index] = entry
		} else {
			out = append(out, entry)
		}
	}
	return out
}

type thinkingPolicyContextKey struct{}

// WithThinkingPolicy freezes the effective policy at the host's turn boundary.
func WithThinkingPolicy(ctx context.Context, policy ModelPolicy) context.Context {
	return context.WithValue(ctx, thinkingPolicyContextKey{}, cloneThinkingOverrides(policy.ThinkingOverrides))
}

func thinkingPolicyFromContext(ctx context.Context) ([]modelcall.ThinkingOverride, bool) {
	value, ok := ctx.Value(thinkingPolicyContextKey{}).([]modelcall.ThinkingOverride)
	return value, ok
}

// ThinkingOverrideError is terminal: retries must not weaken a human override.
type ThinkingOverrideError struct {
	ProviderID string
	Model      string
	Reason     string
}

func (e *ThinkingOverrideError) Error() string {
	return fmt.Sprintf("Thinking override for %s / %s cannot be applied: %s. Review thinking overrides in AI providers.", e.ProviderID, e.Model, e.Reason)
}

func (e *ThinkingOverrideError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeThinkingOverrideUnavailable
}

const maxReasoningCaptureBytes = 512 * 1024

// Buffer complete reasoning for redaction before taking a tail. If the buffer
// fills, omit the text: clipping raw bytes could expose part of a credential.
type reasoningCapture struct {
	bytes int
	text  strings.Builder
}

func (c *reasoningCapture) append(text string) {
	c.bytes += len(text)
	if c.bytes > maxReasoningCaptureBytes {
		c.text.Reset()
		return
	}
	c.text.WriteString(text)
}

func (c *reasoningCapture) summarize(summary *observability.LLMCompletionCapture, err error) *observability.LLMCompletionCapture {
	if c.bytes == 0 {
		return summary
	}
	if summary == nil {
		summary = &observability.LLMCompletionCapture{}
	}
	summary.ReasoningBytes = c.bytes
	_, empty := failure.AsProviderEmptyCompletion(err)
	_, truncated := failure.AsProviderOutputTruncated(err)
	if !empty && !(truncated && summary.ContentChars == 0 && len(summary.ToolCalls) == 0) {
		return summary
	}
	summary.ReasoningCaptureOmitted = c.bytes > maxReasoningCaptureBytes
	if summary.ReasoningCaptureOmitted {
		return summary
	}
	runes := []rune(observability.RedactCaptureText(c.text.String()))
	if len(runes) > 2048 {
		runes = runes[len(runes)-2048:]
	}
	summary.ReasoningTail = string(runes)
	return summary
}
