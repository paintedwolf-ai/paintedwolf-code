package policyindex

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

type Status string

const (
	StatusDisabled    Status = "disabled"
	StatusNoPolicy    Status = "no_policy"
	StatusLoaded      Status = "loaded"
	StatusUnavailable Status = "unavailable"
	StatusScopeLimit  Status = "scope_limit"
)

// Delivery is assembled from current sources for one model request.
type Delivery struct {
	Messages []api.Message
	Manifest agentpolicy.Manifest
	Status   Status
}

// Resolve loads each applicable policy once across all resolved scopes.
func (m *Service) Resolve(ctx context.Context, sess *api.Session, scopes []agentpolicy.Scope) (Delivery, error) {
	if m == nil || m.roots == nil || sess == nil {
		return Delivery{}, fmt.Errorf("policy scope resolver unavailable")
	}
	if !m.roots.Applies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return Delivery{Status: StatusDisabled}, nil
	}
	roots, err := m.sessionRoots(ctx, sess)
	if err != nil {
		return Delivery{}, err
	}
	scopes, err = defaultScopes(roots, sess.WorkspaceRootID, scopes)
	if err != nil {
		return Delivery{}, err
	}
	files, loadErr := m.resolveFiles(ctx, roots, scopes)
	delivery := Delivery{Status: StatusNoPolicy}
	var represented [][]agentpolicy.Revision
	for _, file := range files {
		if m.renderer == nil {
			return Delivery{}, fmt.Errorf("policy renderer unavailable")
		}
		root, _ := projectroot.RootRefByID(roots, file.Revision.RootID)
		label := root.Label
		if label == "" {
			label = root.ID
		}
		block, renderErr := governance.BuildScopedInject(ctx, prompts.NewInjectRenderer(m.renderer), sess.ID, label, file)
		if renderErr != nil {
			return Delivery{}, renderErr
		}
		if strings.TrimSpace(block.Content) == "" {
			return Delivery{}, fmt.Errorf("policy renderer returned an empty block")
		}
		delivery.Messages = append(delivery.Messages, scopedMessage(sess, file, block.Content))
		represented = append(represented, []agentpolicy.Revision{file.Revision})
		delivery.Manifest.Files = append(delivery.Manifest.Files, file.Revision)
	}
	if len(files) > 0 {
		delivery.Status = StatusLoaded
	}
	delivery.Messages = m.recordDeliveries(delivery.Messages, represented)
	if loadErr != nil {
		delivery.Status = StatusUnavailable
	}
	return delivery, loadErr
}

// Current reads applicable revisions without rendering model guidance.
func (m *Service) Current(ctx context.Context, sess *api.Session, scopes []agentpolicy.Scope) (agentpolicy.Manifest, Status, error) {
	if m == nil || m.roots == nil || sess == nil {
		return agentpolicy.Manifest{}, "", fmt.Errorf("policy scope resolver unavailable")
	}
	if !m.roots.Applies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return agentpolicy.Manifest{}, StatusDisabled, nil
	}
	roots, err := m.sessionRoots(ctx, sess)
	if err != nil {
		return agentpolicy.Manifest{}, "", err
	}
	scopes, err = defaultScopes(roots, sess.WorkspaceRootID, scopes)
	if err != nil {
		return agentpolicy.Manifest{}, "", err
	}
	files, loadErr := m.resolveFiles(ctx, roots, scopes)
	manifest := agentpolicy.Manifest{}
	for _, file := range files {
		manifest.Files = append(manifest.Files, file.Revision)
	}
	status := StatusNoPolicy
	if len(files) > 0 {
		status = StatusLoaded
	}
	if loadErr != nil {
		status = StatusUnavailable
	}
	return manifest, status, loadErr
}

func defaultScopes(roots []projectroot.RootRef, activeRootID string, scopes []agentpolicy.Scope) ([]agentpolicy.Scope, error) {
	if len(scopes) > 0 || len(roots) == 0 {
		return scopes, nil
	}
	root, err := projectroot.ActiveRoot(roots, activeRootID)
	if err != nil {
		return nil, err
	}
	return []agentpolicy.Scope{{RootID: root.ID, RootPath: root.Path, Path: "."}}, nil
}

func (m *Service) resolveFiles(ctx context.Context, roots []projectroot.RootRef, scopes []agentpolicy.Scope) ([]governance.ScopedAgentsMD, error) {
	var valid []agentpolicy.Scope
	var failures []error
	for _, scope := range scopes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		root, ok := projectroot.RootRefByID(roots, scope.RootID)
		if !ok {
			failures = append(failures, &agentpolicy.ScopeError{Scope: scope, Cause: fmt.Errorf("policy root %q is not attached", scope.RootID)})
			continue
		}
		if err := sameWorkspace(root.Path, scope.RootPath); err != nil {
			failures = append(failures, &agentpolicy.ScopeError{Scope: scope, Cause: err})
			continue
		}
		valid = append(valid, scope)
	}
	files, err := governance.ResolveScopes(ctx, valid, m.maxBodyBytes())
	failures = append(failures, err)
	sort.SliceStable(files, func(i, j int) bool { return policyBefore(files[i].Revision, files[j].Revision) })
	return files, errors.Join(failures...)
}

func sameWorkspace(bound, supplied string) error {
	if !filepath.IsAbs(bound) || !filepath.IsAbs(supplied) {
		return fmt.Errorf("policy workspace must be absolute")
	}
	actual, err := filepath.EvalSymlinks(bound)
	if err != nil {
		return fmt.Errorf("policy workspace unavailable: %w", err)
	}
	requested, err := filepath.EvalSymlinks(supplied)
	if err != nil {
		return fmt.Errorf("policy workspace unavailable: %w", err)
	}
	if actual != requested {
		return fmt.Errorf("policy workspace does not match the session root")
	}
	return nil
}

func policyBefore(a, b agentpolicy.Revision) bool {
	if a.RootID != b.RootID {
		return a.RootID < b.RootID
	}
	overlay := settingsoverlay.Rel("AGENTS.md")
	if a.Path == overlay || b.Path == overlay {
		return b.Path == overlay && a.Path != overlay
	}
	aDepth, bDepth := strings.Count(a.Path, "/"), strings.Count(b.Path, "/")
	if aDepth != bDepth {
		return aDepth < bDepth
	}
	return a.Path < b.Path
}

func scopedMessage(sess *api.Session, file governance.ScopedAgentsMD, content string) api.Message {
	return api.Message{
		Role: api.MessageRoleSystem, Content: content,
		Origin: api.MessageOriginProject, Authority: api.ContentAuthorityDeveloper, TrustTier: api.ContentTrustTierTrusted,
		SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: sess.ProjectID, RootID: file.Revision.RootID, Path: file.Revision.Path, EntryKind: api.NavigationEntryKindFile}}},
	}
}
