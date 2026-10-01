package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

// conversationCallReader reads a session's latest conversation model call.
type conversationCallReader interface {
	LastConversationCall(ctx context.Context, sessionID string) (cost.ConversationCall, bool, error)
}

// turnBoundary reads whether the provider still holds the chat's standing
// prefix as a turn opens, from recorded facts only.
func (m *Manager) turnBoundary(ctx context.Context, sess *api.Session, recorded bool) turnload.Boundary {
	facts := turnload.BoundaryFacts{
		FirstTurn: !recorded,
		Now:       time.Now().UTC(),
	}
	provider, model, policy, ok := m.nextRoutePolicy(ctx, sess)
	if !ok {
		return turnload.ReadBoundary(facts)
	}
	facts.NextProvider, facts.NextModel = provider, model
	facts.PolicyKnown, facts.Caches = true, policy.Mode.Caches()
	facts.ColdAfter = policy.StandingColdAfter()
	if reader, ok := m.cost.(conversationCallReader); ok {
		if last, found, err := reader.LastConversationCall(ctx, sess.ID); err == nil && found {
			facts.LastCall = true
			facts.LastProvider, facts.LastModel, facts.LastStarted = last.ProviderID, last.Model, last.StartedAt
		}
	}
	if policy.Residency != providerprofile.PromptCacheResidencyUnknown && m.llmSvc != nil && m.llmSvc.Registry != nil {
		facts.Resident, facts.ResidencyKnown = m.llmSvc.Registry.ModelResident(ctx, provider, model)
	}
	return turnload.ReadBoundary(facts)
}

// nextRoutePolicy resolves the route the session's next conversation call
// takes and the prompt-cache policy it runs under.
func (m *Manager) nextRoutePolicy(ctx context.Context, sess *api.Session) (provider, model string, policy providerprofile.PromptCachePolicy, ok bool) {
	if m == nil || m.llmSvc == nil || m.llmSvc.Registry == nil || m.llmSvc.Router == nil || sess == nil {
		return "", "", providerprofile.PromptCachePolicy{}, false
	}
	sel, err := m.llmSvc.Router.WithOverlayRoots(m.overlayRootPaths(ctx, sess)).ResolveSession(ctx, sess)
	if err != nil || sel == nil {
		return "", "", providerprofile.PromptCachePolicy{}, false
	}
	policy, ok = m.llmSvc.Registry.PromptCachePolicy(sel.ProviderID, sel.Model)
	return sel.ProviderID, sel.Model, policy, ok
}

// historyEpoch identifies the compaction view the session's prompt history
// reads; empty when uncompacted. This is receipt provenance, not standing-cache state.
func (m *Manager) historyEpoch(ctx context.Context, sess *api.Session) string {
	if m == nil || m.store == nil || sess == nil {
		return ""
	}
	view, ok, err := m.store.GetCompactionView(ctx, sess.ID)
	if err != nil || !ok || view.Generation != sess.CompactionGeneration {
		return ""
	}
	current, err := m.store.CompactionViewCurrent(ctx, sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq)
	if err != nil || !current {
		return ""
	}
	return fmt.Sprintf("%d:%s", view.Generation, strings.TrimSpace(view.CoveredThroughID))
}
