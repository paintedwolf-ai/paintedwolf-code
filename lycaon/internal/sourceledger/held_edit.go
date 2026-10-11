package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// PreparedHeldEdit retains its published content through transaction completion.
type PreparedHeldEdit interface {
	CommitTx(context.Context, *sql.Tx) (string, error)
	Close()
}

type preparedHeldEdit struct {
	mu      sync.Mutex
	store   *Store
	input   HeldEdit
	object  *db.UpsertSourceBlobObjectParams
	release func()
}

func (s *Store) PrepareHeldEdit(ctx context.Context, in HeldEdit) (PreparedHeldEdit, error) {
	if s == nil || s.sqlDB == nil {
		return nil, fmt.Errorf("ledger not configured")
	}
	in = normalizeHeldEdit(in)
	if in.ProjectID == "" || in.FileID == "" || in.RootID == "" || in.Path == "" {
		return nil, fmt.Errorf("held edit requires project, file, root, and path")
	}
	if in.SHA256 == "" {
		return nil, fmt.Errorf("held edit requires content")
	}
	release := s.objects.AcquireReferenceLease()
	object, err := prepareContent(ctx, s.objects, in.SHA256, in.Content, EntryKindFile)
	if err != nil {
		release()
		return nil, err
	}
	in.Content = nil
	return &preparedHeldEdit{store: s, input: in, object: object, release: release}, nil
}

func (p *preparedHeldEdit) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release != nil {
		p.release()
		p.release = nil
	}
}

// CommitTx retains an unsaved version without moving the working-file head.
func (p *preparedHeldEdit) CommitTx(ctx context.Context, tx *sql.Tx) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release == nil || tx == nil {
		return "", fmt.Errorf("prepared held edit is closed or transaction is missing")
	}
	s, in := p.store, p.input
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
		SHA256: in.SHA256, Object: p.object, Size: in.Size,
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
