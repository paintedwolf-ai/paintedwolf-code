package scan

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/pkg/api"
)

// maxScanGuidanceWatermarks bounds a process-lifetime map keyed by every
// delegation:task pair seen. Past this size the map resets; worst case is one
// redundant re-injection of guidance already shown.
const maxScanGuidanceWatermarks = 4096

// GuidanceProvider prepends ephemeral scan guidance to CompletionRequest messages.
type GuidanceProvider struct {
	Coordinator ScanCoordinator
	Budget      scancfg.AgentBudgetConfig
	injects     *prompts.InjectRenderer
	mu          sync.Mutex
	watermarks  map[string]string // delegation:task -> last injected guidance signature
}

// NewGuidanceProvider constructs a guidance injector.
func NewGuidanceProvider(coord ScanCoordinator, budget scancfg.AgentBudgetConfig) *GuidanceProvider {
	if budget.MaxHintsPerInjection <= 0 {
		budget.MaxHintsPerInjection = scancfg.DefaultAgentBudget().MaxHintsPerInjection
	}
	return &GuidanceProvider{
		Coordinator: coord,
		Budget:      budget,
		watermarks:  map[string]string{},
	}
}

// SetInjectRenderer wires pongo render for inject/scan-guidance-ephemeral.md.
func (p *GuidanceProvider) SetInjectRenderer(renderer *prompts.InjectRenderer) {
	if p == nil {
		return
	}
	p.injects = renderer
}

// Prepend injects one system message when fresh guidance is available.
func (p *GuidanceProvider) Prepend(ctx context.Context, sessionID, delegationID, taskID string, messages []api.Message) []api.Message {
	if p == nil || p.Coordinator == nil {
		return messages
	}
	var summaries []api.ScanGuidanceSummary
	var key string
	if delegationID != "" {
		summaries = p.pending(ctx, delegationID)
		key = delegationID + ":" + taskID
	} else if strings.TrimSpace(sessionID) != "" {
		summaries = p.pendingSession(ctx, sessionID)
		key = "session:" + strings.TrimSpace(sessionID)
	} else {
		return messages
	}
	if len(summaries) == 0 {
		return messages
	}
	sig := guidanceSignature(summaries)
	p.mu.Lock()
	if p.watermarks[key] == sig {
		p.mu.Unlock()
		return messages
	}
	if len(p.watermarks) >= maxScanGuidanceWatermarks {
		p.watermarks = make(map[string]string)
	}
	p.watermarks[key] = sig
	p.mu.Unlock()

	body, err := p.renderGuidance(ctx, sessionID, summaries)
	if err != nil || body == "" {
		return messages
	}
	sys := api.Message{
		Role:    api.MessageRoleSystem,
		Content: body,
	}
	out := make([]api.Message, 0, len(messages)+1)
	out = append(out, sys)
	out = append(out, messages...)
	return out
}

func (p *GuidanceProvider) renderGuidance(ctx context.Context, sessionID string, summaries []api.ScanGuidanceSummary) (string, error) {
	if p == nil || p.injects == nil {
		return "", nil
	}
	rows := make([]map[string]any, 0, len(summaries))
	for _, g := range summaries {
		row := map[string]any{
			"code":    g.Code,
			"message": g.Message,
		}
		if g.Fix != "" {
			row["fix"] = g.Fix
		}
		if g.Count > 1 {
			row["count"] = g.Count
		}
		rows = append(rows, row)
	}
	return anchor.RenderInform(ctx, anchor.InjectScanGuidance, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, p.injects, map[string]any{"summaries": rows})
}

func (p *GuidanceProvider) pending(ctx context.Context, delegationID string) []api.ScanGuidanceSummary {
	categories := []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}
	scan, err := p.Coordinator.LatestForDelegation(ctx, delegationID, categories)
	if err != nil || scan == nil || scan.Status != api.CodeScanStatusComplete {
		return nil
	}
	guidance := scan.Guidance
	cap := p.Budget.MaxHintsPerInjection
	if cap > 0 && len(guidance) > cap {
		guidance = guidance[:cap]
	}
	return guidance
}

func (p *GuidanceProvider) pendingSession(ctx context.Context, sessionID string) []api.ScanGuidanceSummary {
	scans, err := p.Coordinator.ListBySessionID(ctx, sessionID)
	if err != nil {
		return nil
	}
	var latest *api.CodeScan
	for i := range scans {
		sc := &scans[i]
		if sc.Status != api.CodeScanStatusComplete || len(sc.Guidance) == 0 {
			continue
		}
		if !hasSecurityOrSecretCategory(sc.Categories) {
			continue
		}
		if latest == nil || sc.CreatedAt.After(latest.CreatedAt) {
			latest = sc
		}
	}
	if latest == nil {
		return nil
	}
	guidance := latest.Guidance
	cap := p.Budget.MaxHintsPerInjection
	if cap > 0 && len(guidance) > cap {
		guidance = guidance[:cap]
	}
	return guidance
}

func hasSecurityOrSecretCategory(cats []api.ScanCategory) bool {
	for _, cat := range cats {
		if cat == api.ScanCategorySecurity || cat == api.ScanCategorySecret {
			return true
		}
	}
	return false
}

// SessionGuidanceAdapter implements session.ScanGuidanceHook using delegation or session lookup.
type SessionGuidanceAdapter struct {
	Provider            *GuidanceProvider
	DelegationBySession func(sessionID string) (delegationID, taskID string, ok bool)
}

// PrependGuidance prepends ephemeral scan guidance for a coordinator session.
func (a *SessionGuidanceAdapter) PrependGuidance(ctx context.Context, sessionID string, messages []api.Message) []api.Message {
	if a == nil || a.Provider == nil {
		return messages
	}
	var delegationID, taskID string
	if a.DelegationBySession != nil {
		delegationID, taskID, _ = a.DelegationBySession(sessionID)
	}
	return a.Provider.Prepend(ctx, sessionID, delegationID, taskID, messages)
}

func guidanceSignature(in []api.ScanGuidanceSummary) string {
	parts := make([]string, 0, len(in))
	for _, g := range in {
		parts = append(parts, g.Code+":"+g.File)
	}
	return strings.Join(parts, "|")
}
