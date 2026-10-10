package history

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

// ToolWire spills oversized payloads before storage or publication.
// Failed or unavailable compaction leaves the complete observation intact.
func (m *Service) ToolWire(ctx context.Context, sess *api.Session, toolName, content string, opts compaction.CompactToolWireOpts) (string, *api.CompactedChunkMeta) {
	content = strings.TrimSpace(content)
	if content == "" || m == nil || sess == nil || m.Compactor == nil {
		return content, nil
	}
	projectDir, _ := m.Workspace.ActivePath(ctx, sess)

	compact := func(in string) (string, *api.CompactedChunkMeta) {
		if m == nil || sess == nil || m.Compactor == nil {
			return in, nil
		}
		sc, ok := m.Compactor.(*compaction.SimpleCompactor)
		if !ok {
			return in, nil
		}
		providerID, model := m.Model(ctx, sess)
		out, meta, changed, err := sc.CompactToolWireIfOversized(ctx, compaction.SessionInfo{
			ID:                sess.ID,
			ProviderID:        providerID,
			Model:             model,
			AgentType:         sess.AgentType,
			ParentSessionID:   sess.ParentSessionID,
			ProjectID:         sess.ProjectID,
			ProjectDir:        projectDir,
			HostDataDir:       project.HostDataDir(m.dataDir, sess.ProjectID),
			MaxToolSpillBytes: m.Limits.Effective(ctx, sess).MaxToolSpillBytes,
			Posture:           sess.Posture,
		}, toolName, in, opts)
		if err != nil || !changed {
			return in, nil
		}
		return out, &api.CompactedChunkMeta{
			OriginalTokens:  meta.OriginalTokens,
			CompactedTokens: meta.CompactedTokens,
			Kind:            string(meta.Kind),
			Strategy:        meta.Strategy,
			CompactedAt:     meta.At,
		}
	}

	return compact(content)
}
