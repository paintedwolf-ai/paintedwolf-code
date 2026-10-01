package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

// HeldEdit retains accepted agent text that has not reached disk.
type HeldEdit struct {
	ProjectID, FileID, RootID, Path string
	BranchID                        sourcebranch.ID
	Content                         []byte
	SHA256                          string
	Size                            int64
	SessionID                       string
	Turn                            int
	ToolCallID, ToolName            string
	TS                              time.Time
}

// RecordHeldEditTx retains an unsaved version atomically with its document.
// The working-file head stays unchanged.
func (s *Store) RecordHeldEditTx(ctx context.Context, tx *sql.Tx, in HeldEdit) (string, error) {
	if s == nil || s.sqlDB == nil || tx == nil {
		return "", nil
	}
	in = normalizeHeldEdit(in)
	if in.ProjectID == "" || in.FileID == "" || in.RootID == "" || in.Path == "" {
		return "", fmt.Errorf("held edit requires project, file, root, and path")
	}
	if in.SHA256 == "" {
		return "", fmt.Errorf("held edit requires content")
	}
	q := s.queries.WithTx(tx)
	ts := in.TS.Format(time.RFC3339Nano)
	operationID := newID()
	// The document compare-and-swap prevents duplicate records on replay.
	if err := q.InsertSourceOperation(ctx, db.InsertSourceOperationParams{
		ID: operationID, ProjectID: in.ProjectID, BranchID: in.BranchID.String(),
		Origin: string(api.SourceChangeOriginAgent), Cause: CauseAgentEditHeld,
		SessionID: in.SessionID, Turn: int64(in.Turn),
		ToolCallID: in.ToolCallID, ToolName: in.ToolName,
		CaptureQuality: "exact", StartedTs: ts, CommittedTs: ts,
	}); err != nil {
		return "", err
	}
	parentVersionID := ""
	head, err := q.GetSourceBranchHeadByFile(ctx, db.GetSourceBranchHeadByFileParams{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: in.FileID,
	})
	if err == nil {
		parentVersionID = head.VersionID
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return s.insertVersion(ctx, q, versionSpec{
		FileID: in.FileID, ProjectID: in.ProjectID, BranchID: in.BranchID,
		ParentVersionID: parentVersionID, OperationID: operationID,
		RootID: in.RootID, Path: in.Path, EntryKind: EntryKindFile, State: "content",
		SHA256: in.SHA256, Content: in.Content, Size: in.Size,
		CaptureQuality: "exact", Landing: LandingEditorDocument, TS: in.TS,
	})
}

func normalizeHeldEdit(in HeldEdit) HeldEdit {
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.FileID = strings.TrimSpace(in.FileID)
	in.RootID = strings.TrimSpace(in.RootID)
	in.Path = strings.TrimSpace(in.Path)
	if in.TS.IsZero() {
		in.TS = time.Now().UTC()
	}
	if in.SHA256 == "" && in.Content != nil {
		in.SHA256 = sourceblob.ContentSHA(in.Content)
	}
	if in.Size == 0 && in.Content != nil {
		in.Size = int64(len(in.Content))
	}
	return in
}
