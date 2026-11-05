package policyindex

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxObservedScopes = 64

type scopeMemory struct {
	mu       sync.Mutex
	intentID string
	scopes   []agentpolicy.Scope
	limit    *scopeLimitError
}

// ObserveScopes resolves one reached batch and retains its policy jurisdictions.
func (m *Service) ObserveScopes(ctx context.Context, sessionID string, scopes []agentpolicy.Scope) (agentpolicy.Manifest, Status, error) {
	if m == nil || m.store == nil || m.roots == nil {
		return agentpolicy.Manifest{}, "", fmt.Errorf("policy observation store unavailable")
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return agentpolicy.Manifest{}, "", err
	}
	if sess == nil {
		return agentpolicy.Manifest{}, "", fmt.Errorf("policy observation session unavailable")
	}
	manifest, status, err := m.Current(ctx, sess, scopes)
	if ctx.Err() != nil {
		return manifest, status, ctx.Err()
	}
	var remembered []agentpolicy.Scope
	for _, file := range manifest.Files {
		path := filepath.ToSlash(filepath.Dir(file.Path))
		if file.Path == settingsoverlay.Rel("AGENTS.md") {
			path = "."
		}
		remembered = append(remembered, agentpolicy.Scope{RootID: file.RootID, RootPath: file.RootPath, Path: path})
	}
	for _, failure := range policyFailures(err) {
		if failure.Scope.RootID != "" {
			remembered = append(remembered, failure.Scope)
		}
	}
	if limit := m.rememberScopes(sessionID, uniqueScopes(remembered)); limit != nil {
		status = StatusScopeLimit
		err = errors.Join(err, limit)
	}
	return manifest, status, err
}

func (m *Service) rememberScopes(sessionID string, fresh []agentpolicy.Scope) *scopeLimitError {
	state, _ := m.observed.LoadOrStore(sessionID, &scopeMemory{})
	state.mu.Lock()
	defer state.mu.Unlock()
	state.limit = nil
	if len(fresh) > maxObservedScopes {
		state.limit = &scopeLimitError{Count: len(fresh), Limit: maxObservedScopes}
		fresh = fresh[:maxObservedScopes]
	}
	updated := append([]agentpolicy.Scope(nil), fresh...)
	seen := map[agentpolicy.Scope]bool{}
	for _, scope := range updated {
		seen[scope] = true
	}
	for _, old := range state.scopes {
		if !seen[old] && len(updated) < maxObservedScopes {
			updated = append(updated, old)
			seen[old] = true
		}
	}
	state.scopes = updated
	return state.limit
}

// ResolveCurrent combines the active root with current-request source observations.
func (m *Service) ResolveCurrent(ctx context.Context, sess *api.Session, history []api.Message) (Delivery, error) {
	if m == nil || m.roots == nil || sess == nil {
		return Delivery{}, fmt.Errorf("policy scope resolver unavailable")
	}
	if !m.roots.Applies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return Delivery{Status: StatusDisabled}, nil
	}
	roots, err := m.sessionRoots(ctx, sess)
	if err != nil {
		return m.unavailable(ctx, sess, err)
	}
	if len(roots) == 0 {
		return Delivery{Status: StatusNoPolicy}, nil
	}
	active, err := projectroot.ActiveRoot(roots, sess.WorkspaceRootID)
	if err != nil {
		return m.unavailable(ctx, sess, err)
	}
	var scopes []agentpolicy.Scope
	state, _ := m.observed.LoadOrStore(sess.ID, &scopeMemory{})
	intent, _ := api.LastUserIntentMessage(history)
	state.mu.Lock()
	if state.intentID != intent.ID {
		state.intentID, state.scopes = intent.ID, nil
		state.limit = nil
	}
	scopes = append(scopes, state.scopes...)
	limit := state.limit
	state.mu.Unlock()
	scopes = append(scopes, observedScopes(sess.ProjectID, roots, history)...)
	scopes = append(scopes, agentpolicy.Scope{RootID: active.ID, RootPath: active.Path, Path: "."})
	delivery, err := m.Resolve(ctx, sess, uniqueScopes(scopes))
	if err != nil {
		unavailable, diagnosticErr := m.unavailable(ctx, sess, err)
		if diagnosticErr != nil {
			return Delivery{}, diagnosticErr
		}
		delivery.Status = StatusUnavailable
		delivery.Messages = append(delivery.Messages, unavailable.Messages...)
	}
	if limit != nil {
		notice, noticeErr := m.scopeLimit(ctx, sess, *limit)
		if noticeErr != nil {
			return Delivery{}, noticeErr
		}
		delivery.Status = StatusScopeLimit
		delivery.Messages = append(delivery.Messages, notice)
	}
	return delivery, nil
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
