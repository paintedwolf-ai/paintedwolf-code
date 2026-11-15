package assembly

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolschema"
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
func (e *promptSurface) renderSystemPromptWithEngine(ctx context.Context, pe prompts.PromptTemplateEngine, sess *api.Session, templateRef string, vars map[string]any) (string, error) {
	if e == nil || pe == nil {
		return "", errPromptEngineNotConfigured
	}
	agentType := ""
	if sess != nil {
		agentType = strings.TrimSpace(sess.AgentType)
	}
	if fe, ok := pe.(*prompts.FileTemplateEngine); ok && agentType != "" {
		reader, err := e.workerReader(ctx, sess, fe, agentType, vars)
		if err == nil {
			out, renderErr := prompts.RenderPersona(ctx, fe, agentType, reader, vars)
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

func (e *promptSurface) workerReader(ctx context.Context, sess *api.Session, fe *prompts.FileTemplateEngine, agentType string, vars map[string]any) (prompts.Reader, error) {
	profileID := ""
	if agents := e.agentsForSession(ctx, sess); agents != nil {
		if profile, err := agents.Get(agentType); err == nil {
			profileID = strings.TrimSpace(profile.ToolProfile)
		}
	}
	if profileID == "" {
		var err error
		profileID, err = prompts.ToolProfileForAgent(agentType)
		if err != nil {
			return prompts.Reader{}, err
		}
	}
	var (
		profiles []sandbox.ToolProfile
		schemas  *toolschema.Config
		catalog  *extpacks.EffectiveCatalog
	)
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		profiles = view.ToolProfiles
		schemas = view.ToolSchemas
		catalog = view.Catalog
	}
	if catalog == nil && fe != nil {
		catalog = fe.Layers().Catalog
	}
	if len(profiles) == 0 {
		loaded, err := sandbox.LoadToolProfilesWithCatalog(catalog)
		if err != nil {
			return prompts.Reader{}, err
		}
		profiles = loaded
	}
	var visible []string
	var loaded map[string]bool
	rootCount := 1
	if vars != nil {
		if raw, ok := vars["visible_tools"].([]string); ok {
			visible = raw
		}
		if raw, ok := vars["loaded_tools"].(map[string]bool); ok {
			loaded = raw
		}
		if rc, ok := vars["root_count"].(int); ok {
			rootCount = rc
		}
	}
	webSearchEnabled := true
	if fn := e.deps.WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	} else if vars != nil {
		if ws, ok := vars["web_search_enabled"].(bool); ok {
			webSearchEnabled = ws
		}
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID: profileID,
		Profiles:  profiles,
		Loaded:    loaded,
		Visible:   visible,
		Schemas:   schemas,
		RootCount: rootCount,
		WebSearch: webSearchEnabled,
	})
}
