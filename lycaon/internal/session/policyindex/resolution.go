package policyindex

import (
	"context"
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
	StatusDisabled Status = "disabled"
	StatusNoPolicy Status = "no_policy"
	StatusLoaded   Status = "loaded"
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
	roots, err := m.roots.Roots(ctx, sess)
	if err != nil {
		return Delivery{}, err
	}
	if len(scopes) == 0 {
		root, rootErr := projectroot.ActiveRoot(roots, sess.WorkspaceRootID)
		if rootErr != nil {
			return Delivery{}, rootErr
		}
		scopes = []agentpolicy.Scope{{RootID: root.ID, RootPath: root.Path, Path: "."}}
	}
	files, err := m.resolveFiles(ctx, roots, scopes)
	if err != nil {
		return Delivery{}, err
	}
	delivery := Delivery{Status: StatusNoPolicy}
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
		delivery.Messages = append(delivery.Messages, m.recordDelivery(scopedMessage(sess, file, block.Content), file.Revision))
		delivery.Manifest.Files = append(delivery.Manifest.Files, file.Revision)
	}
	if len(files) > 0 {
		delivery.Status = StatusLoaded
	}
	return delivery, nil
}

// Current reads applicable revisions without rendering model guidance.
func (m *Service) Current(ctx context.Context, sess *api.Session, scopes []agentpolicy.Scope) (agentpolicy.Manifest, Status, error) {
	if m == nil || m.roots == nil || sess == nil {
		return agentpolicy.Manifest{}, "", fmt.Errorf("policy scope resolver unavailable")
	}
	if !m.roots.Applies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return agentpolicy.Manifest{}, StatusDisabled, nil
	}
	roots, err := m.roots.Roots(ctx, sess)
	if err != nil {
		return agentpolicy.Manifest{}, "", err
	}
	files, err := m.resolveFiles(ctx, roots, scopes)
	if err != nil {
		return agentpolicy.Manifest{}, "", err
	}
	manifest := agentpolicy.Manifest{}
	for _, file := range files {
		manifest.Files = append(manifest.Files, file.Revision)
	}
	status := StatusNoPolicy
	if len(files) > 0 {
		status = StatusLoaded
	}
	return manifest, status, nil
}

func (m *Service) resolveFiles(ctx context.Context, roots []projectroot.RootRef, scopes []agentpolicy.Scope) ([]governance.ScopedAgentsMD, error) {
	for _, scope := range scopes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		root, ok := projectroot.RootRefByID(roots, scope.RootID)
		if !ok {
			return nil, fmt.Errorf("policy root %q is not attached", scope.RootID)
		}
		if err := sameWorkspace(root.Path, scope.RootPath); err != nil {
			return nil, err
		}
	}
	files, err := governance.ResolveScopes(ctx, scopes, m.maxBodyBytes())
	if err != nil {
		return nil, err
	}
	sort.SliceStable(files, func(i, j int) bool { return policyBefore(files[i].Revision, files[j].Revision) })
	return files, nil
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
