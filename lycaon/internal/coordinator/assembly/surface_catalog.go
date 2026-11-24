package assembly

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// promptSurface resolves and renders one immutable host wiring snapshot.
type promptSurface struct {
	deps  promptSurfaceDeps
	cache *SessionPromptCache
}

func (e *promptSurface) resolveSystemPromptRef(ctx context.Context, sess *api.Session) (string, error) {
	coordinatorProfile := ""
	if e != nil && e.deps.CoordinatorProfile != nil {
		coordinatorProfile = e.deps.CoordinatorProfile(ctx, sess.ID)
	}
	return ResolveSystemPromptTemplate(sess, e.agentsForSession(ctx, sess), coordinatorProfile), nil
}
func (e *promptSurface) agentsForSession(ctx context.Context, sess *api.Session) AgentProfileResolver {
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		return view
	}
	return e.deps.Agents
}
func (e *promptSurface) projectPrompts(ctx context.Context, sess *api.Session) prompts.PromptTemplateEngine {
	pe := e.deps.Prompts
	fe, ok := pe.(*prompts.FileTemplateEngine)
	if !ok || fe == nil || sess == nil {
		return pe
	}
	if archive := e.deps.WorkflowArchive; archive != nil {
		fe = fe.WithWorkflowArchive(archive(ctx, sess.ID))
	}
	// Empty trusted roots disable project prompt layers.
	if resolve := e.deps.ProjectOverlayRootPaths; resolve != nil {
		paths := resolve(ctx, sess)
		if len(paths) == 0 {
			return e.attachSessionCatalog(ctx, sess, fe)
		}
		return e.attachSessionCatalog(ctx, sess, fe.WithProjectOverlays(paths))
	}
	return e.attachSessionCatalog(ctx, sess, fe.WithProjectOverlay(sess.WorkspacePath))
}
func (e *promptSurface) projectPromptsSnapshot(ctx context.Context, sess *api.Session) (prompts.PromptTemplateEngine, string, error) {
	pe := e.projectPrompts(ctx, sess)
	if pe == nil {
		return nil, "", nil
	}
	fe, ok := pe.(*prompts.FileTemplateEngine)
	if !ok {
		return pe, "", nil
	}
	snapshot, err := fe.Snapshot()
	if err != nil {
		return nil, "", fmt.Errorf("prompt source snapshot: %w", err)
	}
	revision := snapshot.Revision()
	return snapshot, revision, nil
}
func (e *promptSurface) attachSessionCatalog(ctx context.Context, sess *api.Session, fe *prompts.FileTemplateEngine) *prompts.FileTemplateEngine {
	if e == nil || fe == nil {
		return fe
	}
	if resolve := e.deps.SessionView; resolve != nil {
		if view := resolve(ctx, sess); view != nil && view.Catalog != nil {
			return fe.WithEffectiveCatalog(view.Catalog)
		}
		return fe
	}
	return fe
}
func (e *promptSurface) sessionCatalogView(ctx context.Context, sess *api.Session) *catalogview.View {
	if e == nil {
		return nil
	}
	if resolve := e.deps.SessionView; resolve != nil {
		return resolve(ctx, sess)
	}
	return nil
}
func (e *promptSurface) renderSystemPromptWithEngine(
	ctx context.Context,
	pe prompts.PromptTemplateEngine,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
	templateRef string,
	vars map[string]any,
) (string, error) {
	if e == nil || pe == nil {
		return "", errPromptEngineNotConfigured
	}
	agentType := ""
	if sess != nil {
		agentType = strings.TrimSpace(sess.AgentType)
	}
	if fe, ok := pe.(*prompts.FileTemplateEngine); ok && agentType != "" {
		reader, err := e.workerTurnReader(ctx, sess, frame, turn)
		if err == nil {
			workerVars := vars
			if vars != nil && vars["agent_skills"] != nil {
				workerVars = maps.Clone(vars)
				filterOccurrenceSkillsForReader(fe, reader, workerVars)
			}
			out, renderErr := prompts.RenderPersona(ctx, fe, agentType, reader, workerVars)
			if renderErr == nil {
				return out, nil
			}
			if !errors.Is(renderErr, prompts.ErrUnknownAgent) {
				return "", renderErr
			}
		} else if !errors.Is(err, prompts.ErrUnknownAgent) {
			return "", err
		}
	}
	return pe.Render(ctx, templateRef, vars)
}

// workerTurnReader is the Reader of the worker request this assembly precedes,
// under the tool profile the session catalog gives the worker's agent.
func (e *promptSurface) workerTurnReader(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
) (prompts.Reader, error) {
	if strings.TrimSpace(frame.Machine.ProfileID) == "" {
		profileID, err := e.workerProfileID(ctx, sess)
		if err != nil {
			return prompts.Reader{}, err
		}
		frame.Machine = inject.Machine{ProfileID: profileID}
	}
	return e.requestReader(ctx, sess, nil, frame)
}

func (e *promptSurface) workerProfileID(ctx context.Context, sess *api.Session) (string, error) {
	agentType := ""
	if sess != nil {
		agentType = strings.TrimSpace(sess.AgentType)
	}
	if agentType == "" {
		agentType = "implementer"
	}
	if agents := e.agentsForSession(ctx, sess); agents != nil {
		if profile, err := agents.Get(agentType); err == nil && strings.TrimSpace(profile.ToolProfile) != "" {
			return strings.TrimSpace(profile.ToolProfile), nil
		}
	}
	return prompts.ToolProfileForAgent(agentType)
}

// commandJobsReader is the request Reader the command-jobs ledger renders
// against, with the wait schema that request carries.
func (e *promptSurface) commandJobsReader(
	ctx context.Context,
	sess *api.Session,
	coordinator bool,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
) (prompts.Reader, map[string]any, error) {
	var (
		reader prompts.Reader
		err    error
	)
	if coordinator {
		if frame.Roster == nil {
			frame.Roster = e.resolveTurnRoster(ctx, sess, frame, history, turn)
		}
		reader, err = e.turnReader(ctx, sess, frame, history, turn)
	} else {
		reader, err = e.workerTurnReader(ctx, sess, frame, turn)
	}
	if err != nil {
		return prompts.Reader{}, nil, err
	}
	return reader, reader.ProjectedSchema("wait"), nil
}
