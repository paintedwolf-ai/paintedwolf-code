package store

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	spillReconcileBatchSize  = 128
	maxIdleSpillRepairStates = 32
	spillReclaimGrace        = time.Minute
)

type messageSpillRef struct {
	ProjectID string
	RelPath   string
}

func writeMessageSpillRefs(ctx context.Context, q *db.Queries, projectID string, msg api.Message) error {
	for _, relPath := range spillPathsForMessage(msg) {
		if err := q.InsertMessageSpillRef(ctx, db.InsertMessageSpillRefParams{
			MessageID: msg.ID,
			ProjectID: projectID,
			RelPath:   relPath,
		}); err != nil {
			return err
		}
	}
	return nil
}

func replaceMessageSpillRefs(ctx context.Context, q *db.Queries, projectID string, msg api.Message) ([]messageSpillRef, error) {
	rows, err := q.ListMessageSpillRefs(ctx, msg.ID)
	if err != nil {
		return nil, err
	}
	oldRefs := spillRefsFromMessageRows(rows)
	if err := q.DeleteMessageSpillRefs(ctx, msg.ID); err != nil {
		return nil, err
	}
	if err := writeMessageSpillRefs(ctx, q, projectID, msg); err != nil {
		return nil, err
	}
	return oldRefs, nil
}

func writeCompactionSpillRefs(ctx context.Context, q *db.Queries, sessionID, projectID string, messages []api.Message) error {
	seen := make(map[string]struct{})
	for _, msg := range messages {
		for _, relPath := range spillPathsForMessage(msg) {
			if _, ok := seen[relPath]; ok {
				continue
			}
			seen[relPath] = struct{}{}
			if err := q.InsertCompactionSpillRef(ctx, db.InsertCompactionSpillRefParams{
				SessionID: sessionID,
				ProjectID: projectID,
				RelPath:   relPath,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func spillPathsForMessage(msg api.Message) []string {
	if msg.Role != api.MessageRoleTool {
		return nil
	}
	seen := make(map[string]struct{})
	var paths []string
	appendPaths := func(content string) {
		for _, relPath := range tooloutput.SpillPaths(content) {
			if _, ok := seen[relPath]; ok {
				continue
			}
			seen[relPath] = struct{}{}
			paths = append(paths, relPath)
		}
	}
	appendPaths(msg.Content)
	if msg.ToolResult != nil {
		appendPaths(msg.ToolResult.Content)
	}
	return paths
}

func reclaimUnreferencedSpills(ctx context.Context, s *sqlCore, refs []messageSpillRef) {
	lifecycle := bloblifecycle.ForDevice(s.dataDir)
	if !lifecycle.TryLock() {
		for _, ref := range refs {
			s.spillReconciled.Delete(ref.ProjectID)
		}
		return
	}
	defer lifecycle.Unlock()
	cutoff := time.Now().Add(-spillReclaimGrace)
	for _, ref := range refs {
		count, err := s.queries.CountMessageSpillRefsByPath(ctx, db.CountMessageSpillRefsByPathParams{
			TargetProjectID: ref.ProjectID,
			TargetRelPath:   ref.RelPath,
		})
		if err != nil {
			slog.WarnContext(ctx, "check tool output reference", "project_id", ref.ProjectID, "path", ref.RelPath, "error", err)
			continue
		}
		if count != 0 {
			continue
		}
		hostDataDir := project.HostDataDir(s.dataDir, ref.ProjectID)
		removed, err := tooloutput.RemoveSpillBefore(hostDataDir, ref.RelPath, cutoff)
		if err != nil {
			slog.WarnContext(ctx, "reclaim tool output", "project_id", ref.ProjectID, "path", ref.RelPath, "error", err)
		} else if !removed {
			s.spillReconciled.Delete(ref.ProjectID)
		}
	}
}

func spillRefsFromTreeRows(rows []db.ListSessionTreeSpillRefsRow) []messageSpillRef {
	out := make([]messageSpillRef, 0, len(rows))
	for _, row := range rows {
		out = appendSpillRef(out, row.ProjectID, row.RelPath)
	}
	return out
}

func spillRefsFromMessageRows(rows []db.ListMessageSpillRefsRow) []messageSpillRef {
	out := make([]messageSpillRef, 0, len(rows))
	for _, row := range rows {
		out = appendSpillRef(out, row.ProjectID, row.RelPath)
	}
	return out
}

func spillRefsFromOrdRows(rows []db.ListSessionSpillRefsFromOrdRow) []messageSpillRef {
	out := make([]messageSpillRef, 0, len(rows))
	for _, row := range rows {
		out = appendSpillRef(out, row.ProjectID, row.RelPath)
	}
	return out
}

func spillRefsFromCompactionRows(rows []db.ListSessionCompactionSpillRefsRow) []messageSpillRef {
	out := make([]messageSpillRef, 0, len(rows))
	for _, row := range rows {
		out = appendSpillRef(out, row.ProjectID, row.RelPath)
	}
	return out
}

func appendSpillRef(refs []messageSpillRef, projectID, relPath string) []messageSpillRef {
	projectID = strings.TrimSpace(projectID)
	relPath = strings.TrimSpace(relPath)
	if projectID == "" || relPath == "" {
		return refs
	}
	return append(refs, messageSpillRef{ProjectID: projectID, RelPath: relPath})
}
