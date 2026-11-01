package assembly

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

func (p *promptSurface) renderSystemPrefix(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame, history []api.Message, turn *TurnAssemblyScratch, vision func(context.Context, *api.Session) bool) (string, error) {
	templateRef, err := p.resolveSystemPromptRef(ctx, sess)
	if err != nil {
		return "", err
	}
	vars := map[string]any{"project_dir": sess.WorkspacePath}
	if roots, count, activePath, ok := p.workspaceRootsForTurn(ctx, sess, turn); ok {
		vars["root_count"] = count
		vars["workspace_roots"] = roots
		if activePath != "" {
			vars["project_dir"] = activePath
		}
	}
	coordinator := surface.IsCoordinatorSession(sess)
	if coordinator {
		vars["skills_heading"] = "##"
	}
	p.applyToolBudgetVars(ctx, sess, coordinator, vars)
	if vision != nil {
		prompts.MergeModelCapabilityVars(vision(ctx, sess), vars)
	}
	coordinatorSurface := coordinator && (!frame.Machine.Compiled() || frame.Machine.ProfileID == orchestration.ProfileCoordinator)
	if !coordinatorSurface {
		p.mergeVisibleTools(ctx, sess, frame, vars)
		p.mergeVisibleToolCapabilityVars(vars)
	}
	p.mergeEffectivePromptSurface(ctx, sess, frame, vars)
	pe, revision, err := p.projectPromptsSnapshot(ctx, sess)
	if err != nil {
		return "", err
	}
	if coordinatorSurface {
		return p.renderCoordinatorStablePrompt(ctx, pe, revision, sess, frame, history, turn, vars)
	}
	rendered, err := p.renderSystemPromptWithEngine(ctx, pe, sess, templateRef, vars)
	if err != nil {
		return "", fmt.Errorf("system prompt %q: %w", templateRef, err)
	}
	return rendered, nil
}

// BuildKnownPrefix projects a supplied frame without delivering turn effects.
func (e *AssemblyEngine) BuildKnownPrefix(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) ([]api.Message, error) {
	if e == nil || sess == nil {
		return nil, nil
	}
	deps := e.deps()
	if deps.Prompts == nil {
		return nil, nil
	}
	cache := &SessionPromptCache{}
	prompt, contextAssembler := newAssemblyDomains(deps, cache)
	turn := &TurnAssemblyScratch{}
	coordinator := surface.IsCoordinatorSession(sess) && !sess.IsWorkerChild()
	if coordinator {
		frame.Roster = prompt.resolveTurnRoster(ctx, sess, frame, history, turn)
	}
	rendered, err := prompt.renderSystemPrefix(ctx, sess, frame, history, turn, deps.ModelVision)
	if err != nil {
		return nil, err
	}
	out := []api.Message{{Role: api.MessageRoleSystem, Content: rendered, ContextPinned: true}}
	out, standing := contextAssembler.appendPreHistorySystemInjects(ctx, sess, turn, out, 0)
	messages := api.FilterPromptHistory(history)
	if deps.EnrichHistory != nil {
		messages = deps.EnrichHistory(sess.ID, messages)
	}
	if coordinator {
		messages = contextAssembler.interleaveSourceBriefs(ctx, sess, turn, messages)
	}
	// Pending scan guidance is delivered only by a real turn.
	out = llm.AppendHostSecretRedactionNotice(append(out, messages...))
	markPromptCacheBreakpoints(out, standing, len(out)-1)
	return transcript.Project(deduplicateProjectGuidance(out)), nil
}
