package llm

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

type preparationSecretScreen interface {
	ScreenPreparation(context.Context, modelcall.CompletionRequest) (bool, error)
}

// CanPrepareContext reports configured local preparation without starting a runtime.
func (r *Registry) CanPrepareContext(providerID string) bool {
	if r == nil {
		return false
	}
	snapshot := r.snapshot.Load()
	id := strings.TrimSpace(providerID)
	if snapshot == nil || !snapshot.configured[id] || snapshot.entries[id].Kind != "nativegen" {
		return false
	}
	_, ok := snapshot.providers[id].(modelcall.ContextPreparer)
	return ok
}

// PrepareContext evaluates a local candidate without starting a model turn.
func (r *Registry) PrepareContext(ctx context.Context, selection *ModelSelection, req modelcall.CompletionRequest) (modelcall.ContextPreparation, error) {
	if r == nil || selection == nil {
		return modelcall.ContextPreparation{}, nil
	}
	snapshot := r.snapshot.Load()
	id := strings.TrimSpace(selection.ProviderID)
	if snapshot == nil || !snapshot.configured[id] || snapshot.entries[id].Kind != "nativegen" {
		return modelcall.ContextPreparation{}, nil
	}
	provider := snapshot.providers[id]
	preparer, ok := provider.(modelcall.ContextPreparer)
	if !ok {
		return modelcall.ContextPreparation{}, nil
	}
	r.screenMu.RLock()
	screen, ok := r.outboundSecretScreen.(preparationSecretScreen)
	r.screenMu.RUnlock()
	if !ok {
		return modelcall.ContextPreparation{}, nil
	}
	req.Model = selection.Model
	req.Messages = transcript.Project(req.Messages)
	req.ControlCapture = nil
	req, err := (&thinkingPolicyProvider{Provider: provider, registry: r}).prepare(ctx, req, nil)
	if err != nil {
		return modelcall.ContextPreparation{}, err
	}
	allowed, err := screen.ScreenPreparation(ctx, req)
	if err != nil || !allowed {
		return modelcall.ContextPreparation{}, err
	}
	return preparer.PrepareContext(ctx, req)
}

// ScreenPreparation skips candidates needing a protected-value decision.
func (s *ModelSecretScreen) ScreenPreparation(ctx context.Context, req modelcall.CompletionRequest) (bool, error) {
	if s == nil {
		return false, nil
	}
	attr := curationctx.SessionFrom(ctx)
	project := firstNonEmpty(req.Debug.ProjectID, attr.ProjectID)
	session := firstNonEmpty(req.Debug.SessionID, attr.SessionID)
	root := firstNonEmpty(req.Debug.RootSessionID, req.Debug.ParentSessionID, attr.ParentSessionID, session)
	if s.evidence != nil && project != "" {
		values, err := s.evidence(ctx, project, root)
		if err != nil {
			return false, &ModelRequestSecretScreenFaultError{Stage: secretmatch.FaultStageManagedSecretStore, Err: err}
		}
		ctx = secretmatch.WithScreeningValues(ctx, values)
	}
	if s.matcher.Inert() {
		return true, nil
	}
	ctx = s.matcher.WithClassifications(ctx, s.matcher.ClassificationSnapshot(ctx))
	return len(inspectModelRequest(ctx, s.matcher, req)) == 0, nil
}
