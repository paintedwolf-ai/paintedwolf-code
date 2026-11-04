package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func checkAgentMutationPolicies(ctx context.Context, tc tools.ToolContext, mutations ...agentMutation) error {
	targets := make([]agentpolicy.Target, 0, 2*len(mutations))
	for _, mutation := range mutations {
		for _, path := range []string{mutation.AbsPath, mutation.FromAbsPath} {
			if path != "" {
				targets = append(targets, agentpolicy.Target{Path: path, Mutation: true})
			}
		}
	}
	return tc.CheckPolicyTargets(ctx, targets)
}

func prepareAgentMutation(ctx context.Context, tctx tools.ToolContext, mutation agentMutation, target, from fseffect.Location, metadata bool) (sourceeffect.Pending, error) {
	if err := checkAgentMutationPolicies(ctx, tctx, mutation); err != nil {
		return nil, err
	}
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

func applyPreparedAgentMutation(ctx context.Context, tc tools.ToolContext, mutation agentMutation, journal sourceeffect.Pending, apply func() error) error {
	err := checkAgentMutationPolicies(ctx, tc, mutation)
	if err == nil {
		err = apply()
	}
	return finishAgentMutation(ctx, tc, mutation, journal, err)
}

func finishAgentMutation(ctx context.Context, tctx tools.ToolContext, mutation agentMutation, journal sourceeffect.Pending, effectErr error) error {
	if effectErr == nil && !mutation.IsDir && !mutation.IsLink && mutation.Op != api.SourceChangeOpDelete {
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
	Memory          *toolcontract.ProgramMemory
	MetadataChange  string
	Op              api.SourceChangeOp
	AbsPath         string
	FromAbsPath     string
	After           []byte
	Before          []byte
	BeforeSHA256    string
	AfterSHA256     string
	BeforeSize      int64
	AfterSize       int64
	IsDir           bool
	IsLink          bool
	Recursive       bool
	Unrecoverable   bool
	RemovalSnapshot *sourceeffect.RemovalSnapshot
	ReviewedFiles   []sourceeffect.ReviewedFile
}

type agentFileEvidence struct {
	release func()
	exists  bool
	isDir   bool
	isLink  bool
	content []byte
	sha256  string
	size    int64
}

func captureAgentFileAt(loc fseffect.Location, ctx context.Context, memory *toolcontract.ProgramMemory, retainBody bool) (agentFileEvidence, error) {
	f, err := fseffect.OpenRead(loc)
	if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		return agentFileEvidence{}, nil
	}
	if err != nil {
		return agentFileEvidence{}, err
	}
	return captureAgentFile(ctx, f, memory, retainBody)
}

// captureAgentFile owns one already-open descriptor and its admitted snapshot.
func captureAgentFile(ctx context.Context, f *os.File, memory *toolcontract.ProgramMemory, retainBody bool) (agentFileEvidence, error) {
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return agentFileEvidence{}, err
	}
	if !info.Mode().IsRegular() {
		return agentFileEvidence{exists: true, isDir: info.IsDir()}, nil
	}
	release, err := (ExecutionRevisions{Memory: memory}).reserveWorking(ctx, 36<<10)
	if err != nil {
		return agentFileEvidence{}, err
	}
	defer release()
	h := sha256.New()
	captured := revisionCapture{ctx: ctx, memory: memory}
	var output io.Writer = h
	if retainBody {
		output = io.MultiWriter(h, &captured)
	}
	scratch := make([]byte, 32<<10)
	size, err := io.CopyBuffer(output, contextio.Reader{Context: ctx, Source: f}, scratch)
	if err != nil {
		captured.Close()
		return agentFileEvidence{}, err
	}
	var content []byte
	if retainBody {
		content = captured.bytes()
	}
	return agentFileEvidence{release: captured.Close, exists: true, content: content, sha256: hex.EncodeToString(h.Sum(nil)), size: size}, nil
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
	return agentCommitInputsWithWorkspace(tctx, m, "")
}

func agentCommitInputsWithWorkspace(tctx tools.ToolContext, m agentMutation, workspaceID string) (sourceledger.RecordInput, sourcefeed.Change, bool) {
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
	isDir := m.IsDir
	if m.IsDir {
		in.EntryKind = sourceledger.EntryKindDirectory
	} else if m.IsLink {
		in.EntryKind = sourceledger.EntryKindLink
	}
	if workspaceID == "" {
		workspaceID = sourceworkspace.ID(tctx.Identity.ProjectID, tctx.Source.Roots)
	}
	change := sourcefeed.Change{
		ProjectID:     tctx.Identity.ProjectID,
		WorkspaceID:   workspaceID,
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
		IsDir:         &isDir,
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
