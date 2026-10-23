package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

type mutationTarget struct {
	Abs      string
	Location fseffect.Location
}

func resolvedMutationTarget(resolved projectpaths.Resolved) mutationTarget {
	return mutationTarget{Abs: resolved.Abs, Location: resolved.EffectLocation()}
}

func ensureIgnoreOverlayFormat(loc fseffect.Location) error {
	rel := filepath.ToSlash(filepath.Clean(loc.Rel))
	target := settingsoverlay.Rel(settingsoverlay.BasenameIgnores)
	if rel == target {
		if strings.TrimSpace(loc.Root) != "" {
			return settingsoverlay.EnsureCurrentFormat(loc.Root)
		}
		return nil
	}
	if strings.HasSuffix(rel, "/"+target) {
		sub := strings.TrimSuffix(rel, "/"+target)
		root := filepath.Join(loc.Root, sub)
		return settingsoverlay.EnsureCurrentFormat(root)
	}
	return nil
}

func applyAgentFile(ctx context.Context, tctx tools.ToolContext, target mutationTarget, after, before []byte, baseSHA256 string) error {
	return applyAgentFileAdmitted(ctx, tctx, target, after, before, baseSHA256, nil)
}

// applyAgentFileAdmitted runs admit after review and the base check, just
// before the reviewed bytes commit, so pinned inputs cannot move during review.
func applyAgentFileAdmitted(ctx context.Context, tctx tools.ToolContext, target mutationTarget, after, before []byte, baseSHA256 string, admit func() error) error {
	if before == nil && baseSHA256 != "" {
		before = []byte{}
	}
	op := api.SourceChangeOpWrite
	if before == nil {
		op = api.SourceChangeOpCreate
	}
	mutation := agentMutation{Op: op, AbsPath: target.Abs, After: after, Before: before,
		BeforeSHA256: hashExistingBytes(before), AfterSHA256: textfile.SHA256(after)}
	var journal sourceeffect.Pending
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: target.Location, Source: bytes.NewReader(after), Mode: 0o644, PreserveMode: true,
		ReviewStaged: func(current fseffect.Target, _ fseffect.Result) error {
			if err := verifyTextWriteBase(current, target.Abs, baseSHA256); err != nil {
				return err
			}
			return reviewAgentMutation(ctx, tctx, mutation, target.Location, fseffect.Location{}, false)
		},
		BeforeCommit: func(current fseffect.Target, _ fseffect.Result) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := ensureIgnoreOverlayFormat(target.Location); err != nil {
				return err
			}
			if err := verifyTextWriteBase(current, target.Abs, baseSHA256); err != nil {
				return err
			}
			if err := verifyMutationSnapshot(ctx, target.Location, mutation.BeforeSHA256, false); err != nil {
				return err
			}
			if admit != nil {
				if err := admit(); err != nil {
					return err
				}
			}
			var err error
			journal, err = prepareAgentMutation(ctx, tctx, mutation, target.Location, fseffect.Location{}, false)
			return err
		},
	})
	return finishAgentMutation(ctx, tctx, mutation, journal, err)
}

type agentStreamRequest struct {
	Memory *toolcontract.ProgramMemory
	// Mode applies to a new file; nil uses the ordinary write mode.
	Mode         *os.FileMode
	Target       mutationTarget
	Source       io.Reader
	BeforeCommit func(fseffect.Target, fseffect.Result) error
}

func applyAgentStream(ctx context.Context, tctx tools.ToolContext, req agentStreamRequest) (fseffect.Result, error) {
	stage, result, err := stageAgentStream(ctx, tctx, req)
	if err != nil {
		return result, err
	}
	defer stage.Close()
	return stage.Commit(ctx, tctx)
}

func stageAgentStream(ctx context.Context, tctx tools.ToolContext, req agentStreamRequest) (*agentStreamStage, fseffect.Result, error) {
	var releaseWorking func()
	if req.Memory != nil {
		release, err := req.Memory.Reserve(ctx, 72<<10)
		if err != nil {
			return nil, fseffect.Result{}, err
		}
		releaseWorking = release
	}
	// Admit fixed stage metadata plus overlapping copy and verification scratch.
	stage := &agentStreamStage{request: req, releaseWorking: releaseWorking}
	if req.Memory != nil {
		stage.copyBuffer = make([]byte, 32<<10)
	}
	ready := false
	defer func() {
		if !ready {
			stage.Close()
		}
	}()

	before, err := captureAgentFileAt(req.Target.Location, ctx, req.Memory, true)
	if err != nil {
		return nil, fseffect.Result{}, err
	}
	op := api.SourceChangeOpWrite
	if !before.exists {
		op = api.SourceChangeOpCreate
	}
	stage.releaseBefore = before.release
	stage.mutation = agentMutation{Op: op, AbsPath: req.Target.Abs,
		Before: before.content, BeforeSHA256: before.sha256, BeforeSize: before.size}
	mutation := &stage.mutation
	stage.captured = revisionCapture{ctx: ctx, memory: req.Memory}
	captured := &stage.captured
	mode := os.FileMode(0o644)
	if req.Mode != nil {
		mode = *req.Mode
	}
	replacement, result, err := fseffect.StageReplace(fseffect.ReplaceRequest{
		Location: req.Target.Location, Source: io.TeeReader(req.Source, captured), Mode: mode, PreserveMode: true, CopyBuffer: stage.copyBuffer,
		ReviewStaged: func(target fseffect.Target, staged fseffect.Result) error {
			if req.BeforeCommit != nil {
				if err := req.BeforeCommit(target, staged); err != nil {
					return err
				}
			}
			mutation.AfterSHA256, mutation.AfterSize = staged.SHA256, staged.Bytes
			mutation.After = captured.bytes()
			if req.Memory != nil {
				// Bound review strings, JSON staging, retained plans and ledger clones
				// while the captured backing capacity remains separately charged.
				reserved := int64(16<<10) + 6*int64(len(mutation.Before)+len(mutation.After)) + 8*int64(len(req.Target.Abs)+len(req.Target.Location.Root)+len(req.Target.Location.Rel))
				release, err := req.Memory.ReserveProgramState(ctx, reserved)
				if err != nil {
					return err
				}
				stage.releasePlan = release
			}
			operation := api.AgentIntentOperationWrite
			if mutation.Op == api.SourceChangeOpCreate {
				operation = api.AgentIntentOperationCreate
			}
			reportMutationIntent(tctx, *mutation, req.Target.Location, fseffect.Location{}, operation)
			return reviewAgentMutation(ctx, tctx, *mutation, req.Target.Location, fseffect.Location{}, false)
		},
		BeforeCommit: func(target fseffect.Target, staged fseffect.Result) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if req.BeforeCommit != nil {
				if err := req.BeforeCommit(target, staged); err != nil {
					return err
				}
			}
			if err := ensureIgnoreOverlayFormat(req.Target.Location); err != nil {
				return err
			}
			if err := verifyMutationSnapshot(ctx, req.Target.Location, mutation.BeforeSHA256, false); err != nil {
				return err
			}
			if err := setAsideUnversionedFile(ctx, tctx, req.Target, mutation); err != nil {
				return err
			}
			var prepareErr error
			if stage.batchPrepared {
				return nil
			}
			stage.journal, prepareErr = prepareAgentMutation(ctx, tctx, *mutation, req.Target.Location, fseffect.Location{}, false)
			return prepareErr
		},
	})
	if err != nil {
		return nil, result, err
	}
	stage.replacement = replacement
	ready = true
	return stage, result, nil
}

// setAsideUnversionedFile keeps a replaced file that is too large for source
// history in recovery before the write lands, so an overwrite never destroys
// the only copy. The write then records as a create.
func setAsideUnversionedFile(ctx context.Context, tctx tools.ToolContext, target mutationTarget, mutation *agentMutation) error {
	if mutation.Op != api.SourceChangeOpWrite || mutation.BeforeSize <= sourceledger.MaxRevisionContentBytes {
		return nil
	}
	if info, err := os.Lstat(target.Abs); err != nil || !info.Mode().IsRegular() {
		return err
	}
	replaced := agentMutation{Op: api.SourceChangeOpDelete, AbsPath: target.Abs,
		BeforeSHA256: mutation.BeforeSHA256, BeforeSize: mutation.BeforeSize}
	if _, ok := recoverableRemoval(tctx, replaced); !ok {
		return nil
	}
	if err := measureAgentRemoval(ctx, tctx, target, &replaced, target.Location.Rel); err != nil {
		return err
	}
	if replaced.Unrecoverable {
		if err := reviewAgentMutation(ctx, tctx, replaced, target.Location, fseffect.Location{}, true); err != nil {
			return err
		}
	}
	if err := discardAgentEntry(ctx, tctx, target, replaced); err != nil {
		return fmt.Errorf("set aside replaced file: %w", err)
	}
	mutation.Op, mutation.Before, mutation.BeforeSHA256, mutation.BeforeSize = api.SourceChangeOpCreate, nil, "", 0
	return nil
}

func removeAgentPath(ctx context.Context, tctx tools.ToolContext, target mutationTarget) error {
	before, err := captureRemovedEntry(ctx, target.Location, true)
	if err != nil {
		return err
	}
	mutation := agentMutation{Op: api.SourceChangeOpDelete, AbsPath: target.Abs,
		Before: before.content, BeforeSHA256: before.sha256, BeforeSize: before.size, IsDir: before.isDir}
	if err := measureAgentRemoval(ctx, tctx, target, &mutation, target.Location.Rel); err != nil {
		return err
	}
	reportMutationIntent(tctx, mutation, target.Location, fseffect.Location{}, api.AgentIntentOperationDelete)
	if err := reviewAgentMutation(ctx, tctx, mutation, target.Location, fseffect.Location{}, mutation.Unrecoverable); err != nil {
		return err
	}
	return discardAgentEntry(ctx, tctx, target, mutation)
}

// discardAgentEntry uses the attached root's reviewed recovery plan;
// outside attached roots it removes the entry through the scoped effect door.
func discardAgentEntry(ctx context.Context, tctx tools.ToolContext, target mutationTarget, mutation agentMutation) error {
	if removal, ok := recoverableRemoval(tctx, mutation); ok {
		operationID, err := tctx.Source.SourceMutations.RemoveEntry(ctx, removal)
		if tctx.Effects.Out != nil && operationID != "" {
			tctx.Effects.Out.OwnerRef = operationID
		}
		var unavailable *sourceeffect.RecoveryUnavailableError
		if !mutation.Unrecoverable && errors.As(err, &unavailable) {
			if reviewErr := reviewUnavailableRemoval(ctx, tctx, target, &mutation); reviewErr != nil {
				return reviewErr
			}
			removal, _ = recoverableRemoval(tctx, mutation)
			operationID, err = tctx.Source.SourceMutations.RemoveEntry(ctx, removal)
		}
		if tctx.Effects.Out != nil && operationID != "" {
			tctx.Effects.Out.OwnerRef = operationID
		}
		if err == nil {
			recordAgentMutationPaths(tctx, mutation)
		}
		return err
	}
	journal, err := prepareAgentMutation(ctx, tctx, mutation, target.Location, fseffect.Location{}, false)
	if err != nil {
		return err
	}
	if mutation.Recursive {
		identity, idErr := fspath.EntryIdentity(target.Abs)
		if idErr != nil {
			_ = finishAgentMutation(ctx, tctx, mutation, journal, idErr)
			return idErr
		}
		return finishAgentMutation(ctx, tctx, mutation, journal, fseffect.RemoveTreeGuarded(target.Location, identity))
	}
	remove := fseffect.RemoveRequest{Location: target.Location}
	if mutation.BeforeSHA256 != "" {
		remove.BeforeCommit = func(current fseffect.Target) error {
			f, openErr := current.Open()
			if openErr != nil {
				return openErr
			}
			defer func() { _ = f.Close() }()
			h := sha256.New()
			if _, copyErr := io.Copy(h, f); copyErr != nil {
				return copyErr
			}
			if hex.EncodeToString(h.Sum(nil)) != mutation.BeforeSHA256 {
				return fmt.Errorf("remove conflict: path changed after inspection")
			}
			return nil
		}
	}
	return finishAgentMutation(ctx, tctx, mutation, journal, fseffect.Remove(remove))
}

func mkdirAgentPath(ctx context.Context, tctx tools.ToolContext, target mutationTarget, mode os.FileMode) (int, error) {
	_, statErr := os.Lstat(target.Abs)
	if statErr == nil {
		return fseffect.MkdirAll(target.Location, mode)
	}
	if !os.IsNotExist(statErr) {
		return 0, statErr
	}
	mutation := agentMutation{Op: api.SourceChangeOpCreate, AbsPath: target.Abs, IsDir: true}
	if err := reviewAgentMutation(ctx, tctx, mutation, target.Location, fseffect.Location{}, false); err != nil {
		return 0, err
	}
	journal, err := prepareAgentMutation(ctx, tctx, mutation, target.Location, fseffect.Location{}, false)
	if err != nil {
		return 0, err
	}
	created, effectErr := fseffect.MkdirAll(target.Location, mode)
	return created, finishAgentMutation(ctx, tctx, mutation, journal, effectErr)
}

func renameAgentPath(ctx context.Context, tctx tools.ToolContext, from, to mutationTarget) error {
	if filepath.Clean(from.Location.Root) != filepath.Clean(to.Location.Root) {
		return fmt.Errorf("rename crosses filesystem effect roots")
	}
	after, err := captureAgentFileAt(from.Location, ctx, nil, true)
	if err != nil {
		return err
	}
	before, err := captureRemovedEntry(ctx, to.Location, true)
	if err != nil {
		return err
	}
	mutation := agentMutation{Op: api.SourceChangeOpRename, AbsPath: to.Abs, FromAbsPath: from.Abs,
		Before: before.content, BeforeSHA256: before.sha256, BeforeSize: before.size,
		After: after.content, AfterSHA256: after.sha256, AfterSize: after.size, IsDir: after.isDir}
	reportMutationIntent(tctx, mutation, to.Location, from.Location, api.AgentIntentOperationMove)
	if err := reviewAgentMutation(ctx, tctx, mutation, to.Location, from.Location, false); err != nil {
		return err
	}
	rename := fseffect.RenameNoReplace
	if before.exists && !before.isDir {
		// The review above showed the replaced file; set it aside recoverably
		// so the move never destroys the only copy.
		displaced := agentMutation{Op: api.SourceChangeOpDelete, AbsPath: to.Abs,
			Before: before.content, BeforeSHA256: before.sha256, BeforeSize: before.size}
		if _, ok := recoverableRemoval(tctx, displaced); ok {
			if err := discardAgentEntry(ctx, tctx, to, displaced); err != nil {
				return fmt.Errorf("set aside replaced destination: %w", err)
			}
			mutation.Before, mutation.BeforeSHA256, mutation.BeforeSize = nil, "", 0
		} else {
			rename = fseffect.Rename
		}
	}
	journal, err := prepareAgentMutation(ctx, tctx, mutation, to.Location, from.Location, false)
	if err != nil {
		return err
	}
	return finishAgentMutation(ctx, tctx, mutation, journal, rename(from.Location.Root, from.Location.Rel, to.Location.Rel))
}

// captureRemovedEntry reads the entry itself: a symbolic link is removed as a
// link, so it carries no content of its own.
func captureRemovedEntry(ctx context.Context, loc fseffect.Location, retainBody bool) (agentFileEvidence, error) {
	info, err := os.Lstat(filepath.Join(loc.Root, loc.Rel))
	if os.IsNotExist(err) {
		return agentFileEvidence{}, nil
	}
	if err != nil {
		return agentFileEvidence{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return agentFileEvidence{exists: true}, nil
	}
	return captureAgentFileAt(loc, ctx, nil, retainBody)
}

// recoverableRemoval is the attributed removal of an entry in an attached
// root on a project or worktree branch. A worker overlay is already a private
// copy its promotion records, and a path outside every root has no history.
func recoverableRemoval(tctx tools.ToolContext, m agentMutation) (sourceeffect.Removal, bool) {
	if tctx.Source.SourceMutations == nil {
		return sourceeffect.Removal{}, false
	}
	in, change, ok := agentCommitInputs(tctx, m)
	if !ok || in.BranchID.IsWorker() {
		return sourceeffect.Removal{}, false
	}
	root, _, located := tctx.SourceLocation(m.AbsPath)
	if !located {
		return sourceeffect.Removal{}, false
	}
	return sourceeffect.Removal{
		Record:           in,
		Change:           change,
		RootPath:         root.Path,
		ReviewedSHA256:   m.BeforeSHA256,
		Recursive:        m.Recursive,
		Unrecoverable:    m.Unrecoverable,
		ReviewedSnapshot: m.RemovalSnapshot,
	}, true
}

func chmodAgentPath(ctx context.Context, tctx tools.ToolContext, target mutationTarget, mode os.FileMode) error {
	return applyAgentMetadataChange(ctx, tctx, target, fmt.Sprintf("Set file permissions to %04o.", mode.Perm()), func() error {
		_, err := fseffect.UpdateMode(fseffect.ModeUpdateRequest{
			Location: target.Location, Update: func(os.FileMode) os.FileMode { return mode },
		})
		return err
	})
}

// timesAgentPath sets access and modification times through the reviewed
// metadata path; a nil time is left unchanged.
func timesAgentPath(ctx context.Context, tctx tools.ToolContext, target mutationTarget, access, modify *time.Time) error {
	return applyAgentMetadataChange(ctx, tctx, target, timesDescription(access, modify), func() error {
		_, err := fseffect.ChangeTimes(fseffect.TimesRequest{Location: target.Location, Access: access, Modify: modify})
		return err
	})
}

func timesDescription(access, modify *time.Time) string {
	parts := make([]string, 0, 2)
	if access != nil {
		parts = append(parts, "access time "+access.UTC().Format(time.RFC3339))
	}
	if modify != nil {
		parts = append(parts, "modification time "+modify.UTC().Format(time.RFC3339))
	}
	return "Set " + strings.Join(parts, " and ") + "."
}

func chownAgentPath(ctx context.Context, tctx tools.ToolContext, target mutationTarget, uid, gid int) error {
	return applyAgentMetadataChange(ctx, tctx, target, fmt.Sprintf("Set file owner to uid %d and gid %d.", uid, gid), func() error {
		_, err := fseffect.ChangeOwnership(fseffect.OwnershipRequest{Location: target.Location, UID: uid, GID: gid})
		return err
	})
}

func applyAgentMetadataChange(ctx context.Context, tctx tools.ToolContext, target mutationTarget, metadataChange string, apply func() error) error {
	evidence, err := captureAgentFileAt(target.Location, ctx, nil, true)
	if err != nil {
		return err
	}
	mutation := agentMutation{Op: api.SourceChangeOpWrite, AbsPath: target.Abs,
		Before: evidence.content, BeforeSHA256: evidence.sha256, BeforeSize: evidence.size,
		After: evidence.content, AfterSHA256: evidence.sha256, AfterSize: evidence.size, IsDir: evidence.isDir, MetadataChange: metadataChange}
	if err := reviewAgentMutation(ctx, tctx, mutation, target.Location, fseffect.Location{}, true); err != nil {
		return err
	}
	journal, err := prepareAgentMutation(ctx, tctx, mutation, target.Location, fseffect.Location{}, true)
	if err != nil {
		return err
	}
	return finishAgentMutation(ctx, tctx, mutation, journal, apply())
}

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
	Recursive       bool
	Unrecoverable   bool
	RemovalSnapshot *sourceeffect.RemovalSnapshot
}

type agentFileEvidence struct {
	release func()
	exists  bool
	isDir   bool
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

// publishAgentStreams closes the durable-intent transaction before touching any
// destination. Each held replacement still revalidates its own target at commit.
func publishAgentStreams(ctx context.Context, tc tools.ToolContext, streams []*agentStreamStage) ([]sourceeffect.Completion, error) {
	plans := make([]sourceeffect.Plan, 0, len(streams))
	for _, stream := range streams {
		record, change, ok := agentCommitInputs(tc, stream.mutation)
		if tc.Source.SourceMutations == nil || !ok {
			return publishIndividualStreams(ctx, tc, streams)
		}
		plans = append(plans, sourceeffect.Plan{Record: record, Change: change, Target: stream.request.Target.Location})
	}
	completions := make([]sourceeffect.Completion, len(streams))
	pending, err := tc.Source.SourceMutations.PrepareEffects(ctx, plans)
	if err != nil {
		return completions, err
	}
	ids := pending.IDs()
	var publicationErr error
	for i, stream := range streams {
		stream.batchPrepared = true
		if tc.Effects.Out != nil {
			tc.Effects.Out.OwnerRef = ids[i]
		}
		completions[i].Attempted = true
		completions[i].Result, completions[i].Err = stream.replacement.Commit()
		if completions[i].Err != nil {
			publicationErr = completions[i].Err
			break
		}
	}
	err = pending.Finish(ctx, completions)
	for i, completion := range completions {
		if completion.Result.Publication.Committed() {
			recordAgentMutationPaths(tc, streams[i].mutation)
			tc.RecordModelAuthoredCredentials(ctx, streams[i].mutation.AbsPath, streams[i].mutation.After)
		}
	}
	return completions, errors.Join(publicationErr, err)
}

// Mixed attribution scopes keep the existing individual-effect owner. This
// path does not add durable history or recovery to an unattached scope.
func publishIndividualStreams(ctx context.Context, tc tools.ToolContext, streams []*agentStreamStage) ([]sourceeffect.Completion, error) {
	completions := make([]sourceeffect.Completion, len(streams))
	for i, stream := range streams {
		completions[i].Attempted = true
		result, err := stream.replacement.Commit()
		completions[i].Result = result
		completions[i].Err = finishAgentMutation(ctx, tc, stream.mutation, stream.journal, err)
		if completions[i].Err != nil {
			return completions, completions[i].Err
		}
	}
	return completions, nil
}

// Commit publishes and settles through the same attributed write owner.
func (s *agentStreamStage) Commit(ctx context.Context, tc tools.ToolContext) (fseffect.Result, error) {
	result, err := s.replacement.Commit()
	return result, finishAgentMutation(ctx, tc, s.mutation, s.journal, err)
}
