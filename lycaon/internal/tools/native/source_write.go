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

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourceeffect"
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
	var delivery *secretcap.PreparedDelivery
	result, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: target.Location, Source: bytes.NewReader(after), Mode: 0o644, PreserveMode: true,
		ReviewStaged: func(current fseffect.Target, _ fseffect.Result) error {
			if err := verifyTextWriteBase(current, target.Abs, baseSHA256); err != nil {
				return err
			}
			if err := reviewAgentMutation(ctx, tctx, mutation, target.Location, fseffect.Location{}, false); err != nil {
				tctx.WithholdFileDelivery(ctx)
				return err
			}
			return nil
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
			if err != nil {
				return err
			}
			delivery, err = tctx.PrepareFileDelivery(ctx)
			if err != nil {
				return err
			}
			return checkAgentMutationPolicies(ctx, tctx, mutation)
		},
	})
	if result.Publication.Committed() {
		delivery.Commit(ctx)
	}
	return finishAgentMutation(ctx, tctx, mutation, journal, err)
}

// newFileMode is a request's creation mode; zero leaves the ordinary mode.
func newFileMode(mode os.FileMode) *os.FileMode {
	if mode == 0 {
		return nil
	}
	return &mode
}

type agentStreamRequest struct {
	Memory *toolcontract.ProgramMemory
	// CopyBuffer is caller-admitted scratch retained until the stage closes.
	CopyBuffer []byte
	// Mode applies to a new file; nil uses the ordinary write mode.
	Mode         *os.FileMode
	Target       mutationTarget
	Source       io.Reader
	KnownAbsent  bool
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
	workingBytes := int64(40 << 10) // fixed metadata and overlapping verification scratch
	if len(req.CopyBuffer) == 0 {
		workingBytes += 32 << 10
	}
	var releaseWorking func()
	if req.Memory != nil {
		release, err := req.Memory.Reserve(ctx, workingBytes)
		if err != nil {
			return nil, fseffect.Result{}, err
		}
		releaseWorking = release
	}
	// Admit fixed stage metadata plus overlapping copy and verification scratch.
	stage := &agentStreamStage{request: req, releaseWorking: releaseWorking, copyBuffer: req.CopyBuffer}
	if req.Memory != nil && len(stage.copyBuffer) == 0 {
		stage.copyBuffer = make([]byte, 32<<10)
	}
	ready := false
	defer func() {
		if !ready {
			stage.Close()
		}
	}()

	var before agentFileEvidence
	if !req.KnownAbsent {
		var err error
		before, err = captureAgentFileAt(req.Target.Location, ctx, req.Memory, true)
		if err != nil {
			return nil, fseffect.Result{}, err
		}
	}
	op := api.SourceChangeOpWrite
	if !before.exists {
		op = api.SourceChangeOpCreate
	}
	stage.releaseBefore = before.release
	stage.mutation = agentMutation{Memory: req.Memory, Op: op, AbsPath: req.Target.Abs,
		Before: before.content, BeforeSHA256: before.sha256, BeforeSize: before.size}
	mutation := &stage.mutation
	stage.captured = revisionCapture{ctx: ctx, memory: req.Memory}
	captured := &stage.captured
	mode := os.FileMode(0o644)
	if req.Mode != nil {
		mode = *req.Mode
	}
	replacement, result, err := fseffect.StageReplace(fseffect.ReplaceRequest{
		Location: req.Target.Location, Source: io.TeeReader(req.Source, captured), Mode: mode, PreserveMode: !req.KnownAbsent, CopyBuffer: stage.copyBuffer,
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
			if err := reviewHeldAgentMutation(ctx, tctx, *mutation, req.Target.Location, target); err != nil {
				tctx.WithholdFileDelivery(ctx)
				return err
			}
			return nil
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
			if err := verifyHeldMutationSnapshot(ctx, req.Target.Location, target, mutation.BeforeSHA256); err != nil {
				return err
			}
			if err := setAsideUnversionedFile(ctx, tctx, req.Target, mutation); err != nil {
				return err
			}
			var prepareErr error
			if !stage.batchPrepared {
				stage.journal, prepareErr = prepareAgentMutation(ctx, tctx, *mutation, req.Target.Location, fseffect.Location{}, false)
				if prepareErr != nil {
					return prepareErr
				}
			}
			stage.delivery, prepareErr = tctx.PrepareFileDelivery(ctx)
			if prepareErr != nil {
				return prepareErr
			}
			return checkAgentMutationPolicies(ctx, tctx, *mutation)
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
	replaced := agentMutation{Memory: mutation.Memory, Op: api.SourceChangeOpDelete, AbsPath: target.Abs,
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

func removeAgentPath(ctx context.Context, tctx tools.ToolContext, target mutationTarget, memory *toolcontract.ProgramMemory) error {
	before, err := captureRemovedEntry(ctx, target.Location, memory, true)
	if err != nil {
		return err
	}

	if before.release != nil {
		defer before.release()
	}
	mutation := agentMutation{Memory: memory, Op: api.SourceChangeOpDelete, AbsPath: target.Abs,
		Before: before.content, BeforeSHA256: before.sha256, BeforeSize: before.size, IsDir: before.isDir, IsLink: before.isLink}
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
		if err := checkAgentMutationPolicies(ctx, tctx, mutation); err != nil {
			return err
		}
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
			if err := checkAgentMutationPolicies(ctx, tctx, mutation); err != nil {
				return err
			}
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
	if mutation.IsLink {
		remove.LinkSHA256 = mutation.BeforeSHA256
	} else if mutation.BeforeSHA256 != "" {
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

func renameAgentPath(ctx context.Context, tctx tools.ToolContext, from, to mutationTarget, memory *toolcontract.ProgramMemory) error {
	return renameAgentEntry(ctx, tctx, from, to, 0, memory, nil)
}

func renameAgentEntry(ctx context.Context, tctx tools.ToolContext, from, to mutationTarget, displacedBytes int64, memory *toolcontract.ProgramMemory, reviewDisplacedPolicy func() ([]sourceeffect.ReviewedFile, error)) error {
	if filepath.Clean(from.Location.Root) != filepath.Clean(to.Location.Root) {
		return fmt.Errorf("rename crosses filesystem effect roots")
	}
	after, err := captureAgentFileAt(from.Location, ctx, memory, true)
	if err != nil {
		return err
	}
	if after.release != nil {
		defer after.release()
	}
	before, err := captureRemovedEntry(ctx, to.Location, memory, true)
	if err != nil {
		return err
	}
	if before.release != nil {
		defer before.release()
	}
	beforeSize := before.size
	if before.isDir && displacedBytes > 0 {
		beforeSize = displacedBytes
	}
	mutation := agentMutation{Memory: memory, Op: api.SourceChangeOpRename, AbsPath: to.Abs, FromAbsPath: from.Abs,
		Before: before.content, BeforeSHA256: before.sha256, BeforeSize: beforeSize,
		After: after.content, AfterSHA256: after.sha256, AfterSize: after.size, IsDir: after.isDir}
	reportMutationIntent(tctx, mutation, to.Location, from.Location, api.AgentIntentOperationMove)
	if err := reviewAgentMutation(ctx, tctx, mutation, to.Location, from.Location, false); err != nil {
		return err
	}
	rename := fseffect.RenameNoReplace
	if before.exists && before.isDir == after.isDir {
		// The review above showed the replaced destination; set it aside recoverably
		// so the move never destroys the only copy.
		displaced := agentMutation{Memory: memory, Op: api.SourceChangeOpDelete, AbsPath: to.Abs,
			Before: before.content, BeforeSHA256: before.sha256, BeforeSize: beforeSize, IsDir: before.isDir, Recursive: before.isDir}
		if reviewDisplacedPolicy != nil {
			reviewed, err := reviewDisplacedPolicy()
			if err != nil {
				return err
			}
			displaced.ReviewedFiles = reviewed
		}
		if _, ok := recoverableRemoval(tctx, displaced); ok {
			if err := measureAgentRemoval(ctx, tctx, to, &displaced, to.Location.Rel); err != nil {
				return err
			}
			if displaced.Unrecoverable {
				if err := reviewAgentMutation(ctx, tctx, displaced, to.Location, fseffect.Location{}, true); err != nil {
					return err
				}
			}
			if err := discardAgentEntry(ctx, tctx, to, displaced); err != nil {
				return fmt.Errorf("set aside replaced destination: %w", err)
			}
			mutation.Before, mutation.BeforeSHA256, mutation.BeforeSize = nil, "", 0
		} else if !before.isDir {
			rename = fseffect.Rename
		} else {
			identity, idErr := fspath.EntryIdentity(to.Abs)
			if idErr != nil {
				return idErr
			}
			if err := fseffect.RemoveTreeGuarded(to.Location, identity); err != nil {
				return err
			}
		}
	}
	journal, err := prepareAgentMutation(ctx, tctx, mutation, to.Location, from.Location, false)
	if err != nil {
		return err
	}
	return finishAgentMutation(ctx, tctx, mutation, journal, rename(from.Location.Root, from.Location.Rel, to.Location.Rel))
}

// captureRemovedEntry reads the entry itself, including literal link text.
func captureRemovedEntry(ctx context.Context, loc fseffect.Location, memory *toolcontract.ProgramMemory, retainBody bool) (agentFileEvidence, error) {

	info, err := os.Lstat(filepath.Join(loc.Root, loc.Rel))
	if os.IsNotExist(err) {
		return agentFileEvidence{}, nil
	}
	if err != nil {
		return agentFileEvidence{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		root, openErr := os.OpenRoot(loc.Root)
		if openErr != nil {
			return agentFileEvidence{}, openErr
		}
		defer root.Close()
		target, readErr := root.Readlink(loc.Rel)
		if readErr != nil {
			return agentFileEvidence{}, readErr
		}
		content := []byte(target)
		return agentFileEvidence{exists: true, isLink: true, content: content, sha256: textfile.SHA256(content), size: int64(len(content))}, nil
	}
	return captureAgentFileAt(loc, ctx, memory, retainBody)
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
		Memory:           m.Memory,
		Record:           in,
		Change:           change,
		RootPath:         root.Path,
		ReviewedSHA256:   m.BeforeSHA256,
		ReviewedFiles:    m.ReviewedFiles,
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

// publishAgentStreams closes the durable-intent transaction before touching any
// destination. Each held replacement still revalidates its own target at commit.
func publishAgentStreams(ctx context.Context, tc tools.ToolContext, memory *toolcontract.ProgramMemory, streams []*agentStreamStage) ([]sourceeffect.Completion, sourceeffect.PendingEffects, error) {
	plans := make([]sourceeffect.Plan, 0, len(streams))
	mutations := make([]agentMutation, 0, len(streams))
	var workspaceID string
	if tc.Identity.ProjectID != "" {
		workspaceID = sourceworkspace.ID(tc.Identity.ProjectID, tc.Source.Roots)
	}
	for _, stream := range streams {
		record, change, ok := agentCommitInputsWithWorkspace(tc, stream.mutation, workspaceID)
		if tc.Source.SourceMutations == nil || !ok {
			completions, err := publishIndividualStreams(ctx, tc, streams)
			return completions, nil, err
		}
		plans = append(plans, sourceeffect.Plan{Record: record, Change: change, Target: stream.request.Target.Location})
		mutations = append(mutations, stream.mutation)
	}
	completions := make([]sourceeffect.Completion, len(streams))
	if err := checkAgentMutationPolicies(ctx, tc, mutations...); err != nil {
		return completions, nil, err
	}
	pending, err := tc.Source.SourceMutations.PrepareEffects(ctx, memory, plans)
	if err != nil {
		return completions, nil, err
	}
	ids := pending.IDs()
	var publicationErr error
	for i, stream := range streams {
		stream.batchPrepared = true
		if tc.Effects.Out != nil {
			tc.Effects.Out.OwnerRef = ids[i]
		}
		completions[i].Attempted = true
		completions[i].Result, completions[i].Err = stream.publish(ctx)
		if completions[i].Err != nil {
			publicationErr = completions[i].Err
			break
		}
	}
	err = pending.Record(ctx, completions)
	if err != nil {
		pending.Close()
		pending = nil
	}
	for i, completion := range completions {
		if completion.Result.Publication.Committed() {
			recordAgentMutationPaths(tc, streams[i].mutation)
			tc.RecordModelAuthoredCredentials(ctx, streams[i].mutation.AbsPath, streams[i].mutation.After)
		}
	}
	return completions, pending, errors.Join(publicationErr, err)
}

// Mixed attribution scopes keep the existing individual-effect owner. This
// path does not add durable history or recovery to an unattached scope.
func publishIndividualStreams(ctx context.Context, tc tools.ToolContext, streams []*agentStreamStage) ([]sourceeffect.Completion, error) {
	completions := make([]sourceeffect.Completion, len(streams))
	for i, stream := range streams {
		completions[i].Attempted = true
		result, err := stream.publish(ctx)
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
	result, err := s.publish(ctx)
	return result, finishAgentMutation(ctx, tc, s.mutation, s.journal, err)
}
