package policyindex

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) forPaths(ctx context.Context, sess *api.Session, paths []string) (api.Message, error) {
	roots, err := m.roots.Roots(ctx, sess)
	if err != nil {
		return api.Message{}, err
	}
	var scopes []agentpolicy.Scope
	for _, path := range paths {
		path = governance.FirstConcreteScopePath([]string{path})
		abs, root, resolveErr := projectroot.ResolveAbs(roots, sess.WorkspaceRootID, path)
		if resolveErr != nil {
			return api.Message{}, resolveErr
		}
		rel, relErr := filepath.Rel(root.Path, abs)
		if relErr != nil {
			return api.Message{}, relErr
		}
		scopes = append(scopes, agentpolicy.Scope{RootID: root.ID, RootPath: root.Path, Path: rel})
	}
	delivery, err := m.Resolve(ctx, sess, scopes)
	if err != nil || len(delivery.Messages) == 0 {
		return api.Message{}, err
	}
	message := delivery.Messages[0]
	var content []string
	var sources []*api.SourceContext
	for _, block := range delivery.Messages {
		content = append(content, block.Content)
		sources = append(sources, block.SourceContext)
	}
	message.Content = strings.Join(content, "\n\n")
	message.SourceContext = sourceref.Merge(sources...)
	return m.recordDelivery(message, delivery.Manifest.Files...), nil
}
