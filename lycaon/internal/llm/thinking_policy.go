package llm

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	wire "github.com/lycaon/lycaon/pkg/api"
)

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
