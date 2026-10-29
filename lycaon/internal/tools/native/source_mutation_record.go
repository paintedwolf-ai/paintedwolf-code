package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"os"
)

func prepareAgentMutation(ctx context.Context, tctx tools.ToolContext, mutation agentMutation, target, from fseffect.Location, metadata bool) (sourceeffect.Pending, error) {
	if tctx.Source.SourceMutations == nil {
		return nil, nil
	}
	in, change, ok := agentCommitInputs(tctx, mutation)
	if !ok {
		return nil, nil
	}
	journal, err := tctx.Source.SourceMutations.PrepareEffect(ctx, sourceeffect.Plan{
		Record: in, Change: change, Target: target, From: from, MetadataOnly: metadata,
	})
	if err == nil && tctx.Effects.Out != nil {
		tctx.Effects.Out.OwnerRef = journal.ID()
	}
	return journal, err
}

func finishAgentMutation(ctx context.Context, tctx tools.ToolContext, mutation agentMutation, journal sourceeffect.Pending, effectErr error) error {
	if effectErr == nil && !mutation.IsDir && mutation.Op != api.SourceChangeOpDelete {
		tctx.RecordModelAuthoredCredentials(ctx, mutation.AbsPath, mutation.After)
	}
	if journal != nil {
		err := journal.Finish(ctx, effectErr)
		if effectErr == nil {
			recordAgentMutationPaths(tctx, mutation)
		}
		return err
	}
	if effectErr != nil {
		return effectErr
	}
	return commitAgentMutation(context.WithoutCancel(ctx), tctx, mutation)
}

type agentMutation struct {
	MetadataChange string
	Op             api.SourceChangeOp
	AbsPath        string
	FromAbsPath    string
	After          []byte
	Before         []byte
	BeforeSHA256   string
	AfterSHA256    string
	BeforeSize     int64
	AfterSize      int64
	IsDir          bool
}

type agentFileEvidence struct {
	exists  bool
	isDir   bool
	content []byte
	sha256  string
	size    int64
}

func captureAgentFileAt(loc fseffect.Location) (agentFileEvidence, error) {
	f, err := fseffect.OpenRead(loc)
	if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		return agentFileEvidence{}, nil
	}
	if err != nil {
		return agentFileEvidence{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return agentFileEvidence{}, err
	}
	if !info.Mode().IsRegular() {
		return agentFileEvidence{exists: true, isDir: info.IsDir()}, nil
	}
	h := sha256.New()
	var captured revisionCapture
	size, err := io.Copy(io.MultiWriter(h, &captured), f)
	if err != nil {
		return agentFileEvidence{}, err
	}
	return agentFileEvidence{exists: true, content: captured.bytes(), sha256: hex.EncodeToString(h.Sum(nil)), size: size}, nil
}

func hashExistingBytes(content []byte) string {
	if content == nil {
		return ""
	}
	return textfile.SHA256(content)
}

type txLedger interface {
	LedgerDB() db.Handle
}

// Attribution and its source event commit as one fact.
func commitAgentMutation(ctx context.Context, tctx tools.ToolContext, m agentMutation) error {
	recordAgentMutationPaths(tctx, m)
	in, change, ok := agentCommitInputs(tctx, m)
	if !ok {
		return nil
	}
	if database := agentLedgerDB(tctx.Source.SourceLedger); database != nil {
		return commitAgentMutationTx(ctx, database, tctx.Source.SourceLedger, in, change)
	}
	return commitAgentMutationWithoutStore(ctx, tctx.Source.SourceLedger, in, change)
}

// Paths outside attached roots produce no durable mutation.
func agentCommitInputs(tctx tools.ToolContext, m agentMutation) (sourceledger.RecordInput, sourcefeed.Change, bool) {
	root, path, located := tctx.SourceLocation(m.AbsPath)
	if tctx.Identity.ProjectID == "" || !located {
		return sourceledger.RecordInput{}, sourcefeed.Change{}, false
	}
	fromPath := ""
	if m.FromAbsPath != "" {
		if _, from, ok := tctx.SourceLocation(m.FromAbsPath); ok {
			fromPath = from
		}
	}
	// Branchless worker calls produce no durable mutation.
	branch, branchErr := tctx.SourceBranch(root.ID)
	if branchErr != nil {
		return sourceledger.RecordInput{}, sourcefeed.Change{}, false
	}
	in := sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: root.ID, Path: path, FromPath: fromPath, EntryKind: sourceledger.EntryKindFile},
		ProjectID:      tctx.Identity.ProjectID,
		BranchID:       branch,
		Op:             m.Op, Origin: api.SourceChangeOriginAgent,
		SessionID: tctx.Identity.SessionID, JobID: tctx.Identity.WorkerJobID, ToolCallID: tctx.Identity.ToolCallID,
		ToolName:     tctx.Invocation.ToolName,
		Turn:         tctx.Identity.UserTurn,
		BeforeSHA256: m.BeforeSHA256, AfterSHA256: m.AfterSHA256, Before: m.Before, After: m.After,
		BeforeSize: max(m.BeforeSize, int64(len(m.Before))), AfterSize: max(m.AfterSize, int64(len(m.After)))}
	var isDir *bool
	if m.IsDir {
		value := true
		isDir = &value
		in.EntryKind = sourceledger.EntryKindDirectory
	}
	change := sourcefeed.Change{
		ProjectID:     tctx.Identity.ProjectID,
		WorkspaceID:   sourceworkspace.ID(tctx.Identity.ProjectID, tctx.Source.Roots),
		WorkspaceKind: tctx.Source.SourceWorkspaceKind,
		RootID:        root.ID,
		Path:          path,
		FromPath:      fromPath,
		Op:            m.Op,
		Origin:        api.SourceChangeOriginAgent,
		SessionID:     tctx.Identity.SessionID,
		JobID:         tctx.Identity.WorkerJobID,
		ToolCallID:    tctx.Identity.ToolCallID,
		Turn:          tctx.Identity.UserTurn,
		AfterSHA256:   m.AfterSHA256,
		IsDir:         isDir,
		AbsPath:       m.AbsPath,
		FromAbsPath:   m.FromAbsPath,
	}
	return in, change, true
}

func agentLedgerDB(ledger sourceledger.Recorder) db.Handle {
	source, ok := ledger.(txLedger)
	if !ok {
		return nil
	}
	return source.LedgerDB()
}

func commitAgentMutationTx(
	ctx context.Context,
	database db.Handle,
	ledger sourceledger.Recorder,
	in sourceledger.RecordInput,
	change sourcefeed.Change,
) error {
	prepared, err := ledger.Prepare(ctx, []sourceledger.RecordInput{in})
	if err != nil {
		return fmt.Errorf("source ledger preparation failed after successful write: %w", err)
	}
	defer prepared.Close()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("source change transaction failed after successful write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := prepared.CommitTx(ctx, tx); err != nil {
		return fmt.Errorf("source ledger record failed after successful write: %w", err)
	}
	delivery, err := sourcefeed.EmitTx(ctx, tx, change)
	if err != nil {
		return fmt.Errorf("source change announce failed after successful write: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("source change commit failed after successful write: %w", err)
	}
	delivery.DeliverCommitted()
	return nil
}

// Record before announcing when no transaction is available.
func commitAgentMutationWithoutStore(
	ctx context.Context,
	ledger sourceledger.Recorder,
	in sourceledger.RecordInput,
	change sourcefeed.Change,
) error {
	if ledger != nil {
		if err := ledger.Record(ctx, in); err != nil {
			return fmt.Errorf("source ledger record failed after successful write: %w", err)
		}
	}
	if err := sourcefeed.Emit(ctx, change); err != nil {
		return fmt.Errorf("source change announce failed after successful write: %w", err)
	}
	return nil
}

func recordAgentMutationPaths(tctx tools.ToolContext, m agentMutation) {
	kind := api.NavigationEntryKindFile
	if m.IsDir {
		kind = api.NavigationEntryKindFolder
	}
	tctx.RecordSourcePath(m.AbsPath, kind)
	if m.FromAbsPath != "" {
		tctx.RecordSourcePath(m.FromAbsPath, kind)
	}
}
