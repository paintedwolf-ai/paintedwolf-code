package history

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) Assemble(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string) ([]api.Message, promptassembly.Report) {
	resolvedSess := sess
	if sess != nil {
		if projectDir, err := m.Workspace.ActivePath(ctx, sess); err == nil && projectDir != "" {
			copy := *sess
			copy.WorkspacePath = projectDir
			resolvedSess = &copy
		}
	}
	deps := promptassembly.Config{
		SurfaceID: surfaceID,
	}
	if m != nil {
		deps.CompactionConfig = m.Limits.Compaction(ctx, sess)
	} else {
		deps.CompactionConfig = compaction.DefaultCompactionConfig()
	}
	if sess != nil && sess.IsWorkerChild() {
		deps.CompactionConfig = compaction.WorkerChildCompactionConfig(deps.CompactionConfig)
	}
	if sess != nil {
		deps.TokenCalibration = m.Calibration(sess.ID)
	}
	deps.ColdStartOverhead = deps.CompactionConfig.ColdStartOverhead(compaction.ResolveColdStartOverhead(""))
	if len(history) == 0 {
		return history, promptassembly.Report{}
	}
	history = m.ApplyView(ctx, resolvedSess, history)
	deps.CompactionViewApplied = true
	return promptassembly.Assemble(resolvedSess, history, deps)
}

// Reload loads canonical messages and returns the dieted projection.
func (m *Service) Reload(ctx context.Context, sessionID string, sess *api.Session, surfaceID string) ([]api.Message, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("session store not configured")
	}
	if sess == nil {
		return nil, fmt.Errorf("session required for reload assemble")
	}
	msgs, err := m.Load(ctx, sess)
	if err != nil {
		return nil, err
	}
	assembled, _ := m.Assemble(ctx, sess, msgs, surfaceID)
	return assembled, nil
}
