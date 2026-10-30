package policyindex

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxObservedScopes = 64

type scopeMemory struct {
	mu       sync.Mutex
	intentID string
	scopes   []agentpolicy.Scope
}

// Observe remembers a reached scope for the next completion, including held effects.
func (m *Service) Observe(ctx context.Context, sessionID string, scope agentpolicy.Scope) error {
	if m == nil || m.store == nil || m.roots == nil {
		return fmt.Errorf("policy observation store unavailable")
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("policy observation session unavailable: %w", err)
	}
	if sess == nil {
		return fmt.Errorf("policy observation session unavailable")
	}
	roots, err := m.roots.Roots(ctx, sess)
	if err != nil {
		return err
	}
	root, ok := projectroot.RootRefByID(roots, scope.RootID)
	if !ok {
		return fmt.Errorf("policy root %q is not attached", scope.RootID)
	}
	if err := sameWorkspace(root.Path, scope.RootPath); err != nil {
		return err
	}
	if _, err := projectroot.SessionPath(root.Path, scope.Path); err != nil {
		return err
	}
	state, _ := m.observed.LoadOrStore(sessionID, &scopeMemory{})
	state.mu.Lock()
	defer state.mu.Unlock()
	updated := []agentpolicy.Scope{scope}
	for _, old := range state.scopes {
		if old != scope && len(updated) < maxObservedScopes {
			updated = append(updated, old)
		}
	}
	state.scopes = updated
	return nil
}

// ResolveCurrent combines the active root with current-request source observations.
func (m *Service) ResolveCurrent(ctx context.Context, sess *api.Session, history []api.Message) (Delivery, error) {
	if m == nil || m.roots == nil || sess == nil {
		return Delivery{}, fmt.Errorf("policy scope resolver unavailable")
	}
	if !m.roots.Applies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return Delivery{Status: StatusDisabled}, nil
	}
	roots, err := m.roots.Roots(ctx, sess)
	if err != nil {
		return Delivery{}, err
	}
	if len(roots) == 0 {
		return Delivery{Status: StatusNoPolicy}, nil
	}
	active, err := projectroot.ActiveRoot(roots, sess.WorkspaceRootID)
	if err != nil {
		return Delivery{}, err
	}
	scopes := []agentpolicy.Scope{{RootID: active.ID, RootPath: active.Path, Path: "."}}
	state, _ := m.observed.LoadOrStore(sess.ID, &scopeMemory{})
	intent, _ := api.LastUserIntentMessage(history)
	state.mu.Lock()
	if state.intentID != intent.ID {
		state.intentID, state.scopes = intent.ID, nil
	}
	scopes = append(scopes, state.scopes...)
	state.mu.Unlock()
	scopes = append(scopes, observedScopes(sess.ProjectID, roots, history)...)
	return m.Resolve(ctx, sess, uniqueScopes(scopes))
}

func observedScopes(projectID string, roots []projectroot.RootRef, history []api.Message) []agentpolicy.Scope {
	var scopes []agentpolicy.Scope
	for i, boundary := len(history)-1, api.UserIntentBoundary(history); i >= boundary && len(scopes) < maxObservedScopes; i-- {
		message := history[i]
		if message.Role != api.MessageRoleTool || message.Origin != api.MessageOriginTool || message.SourceContext == nil || message.ToolResult == nil {
			continue
		}
		result := message.ToolResult
		if result.Invocation == nil || result.ToolCallID == "" || result.ToolCallID != result.Invocation.ToolCallID || result.Tool != result.Invocation.Tool {
			continue
		}
		for _, location := range message.SourceContext.Locations {
			root, ok := projectroot.RootRefByID(roots, location.RootID)
			if !ok || location.ProjectID != projectID || !sourceref.Valid(location) || len(scopes) >= maxObservedScopes {
				continue
			}
			scopes = append(scopes, agentpolicy.Scope{RootID: root.ID, RootPath: root.Path, Path: filepath.FromSlash(location.Path)})
		}
	}
	return scopes
}

func uniqueScopes(scopes []agentpolicy.Scope) []agentpolicy.Scope {
	seen := map[agentpolicy.Scope]bool{}
	var out []agentpolicy.Scope
	for _, scope := range scopes {
		if !seen[scope] {
			seen[scope] = true
			out = append(out, scope)
		}
	}
	return out
}
