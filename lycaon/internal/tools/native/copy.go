package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// CopyTool copies regular files, and bounded directory trees, within write scope.
type CopyTool struct {
	Boundary *sandbox.Boundary
	// Caps overrides the catalog recursive mutation ceilings; zero uses them.
	Caps RecursiveCaps
}

type copyResult struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Bytes int64  `json:"bytes"`
}

type copyResponse struct {
	Copied []copyResult `json:"copied"`
}

// CopyInput is one complete copy operation. Recursive copies a directory tree
// parents first, measured against the catalog's recursive mutation caps before
// any entry is written. NoClobber keeps existing destination files.
type CopyInput struct {
	Pairs     []FilePair
	Recursive bool
	NoClobber bool
	// Preserve copies permission bits and access/modification times.
	Preserve     bool
	MaxFileBytes int64
}

// CopyResult lists written files in order, including those already written
// when a later entry fails.
type CopyResult struct {
	// Effects counts observed file publications and directory creations, even on error.
	Effects int
	Copied  []copyResult
	Skipped []FilePair
}

func (t *CopyTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pairs, maxBytes, err := parseCopyPairs(args)
	if err != nil {
		return "", err
	}
	result, err := t.Copy(ctx, CopyInput{Pairs: pairs, MaxFileBytes: maxBytes}, tctx)
	resp := copyResponse{Copied: result.Copied}
	if resp.Copied == nil {
		resp.Copied = []copyResult{}
	}
	if err != nil {
		if len(resp.Copied) > 0 {
			return interruptedMutationBatch(resp, err)
		}
		return "", err
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// CopyOperation keeps recursive admission across every source in one utility
// invocation, including sources handled after an ordinary operand failure.
type CopyOperation struct {
	memory         *toolcontract.ProgramMemory
	reads          *projectpaths.ReadSession
	tool           *CopyTool
	input          CopyInput
	caps, used     RecursiveCaps
	maxFileBytes   int64
	resultReleases []func()
	pending        []sourceeffect.PendingEffects
	pendingRecords int
	pendingBytes   int64
	pendingRelease func()
}

func (t *CopyTool) Begin(in CopyInput, memory *toolcontract.ProgramMemory) (*CopyOperation, error) {
	if memory == nil {
		limits, err := toolcontract.CatalogProgramMemoryLimits()
		if err != nil {
			return nil, err
		}
		memory, err = toolcontract.NewProgramMemory(limits)
		if err != nil {
			return nil, err
		}
	}
	caps, err := t.Caps.resolve()
	if err != nil {
		return nil, err
	}
	maxBytes := in.MaxFileBytes
	if maxBytes <= 0 {
		if in.Recursive {
			maxBytes = caps.Bytes
		} else {
			maxBytes = defaultCopyMaxBytes
		}
	}
	in.Pairs = nil
	return &CopyOperation{memory: memory, tool: t, input: in, caps: caps, maxFileBytes: maxBytes}, nil
}

// Copy writes a complete batch through one operation's mutation streams.
func (t *CopyTool) Copy(ctx context.Context, in CopyInput, tctx tools.ToolContext) (CopyResult, error) {
	var result CopyResult
	op, err := t.Begin(in, nil)
	if err != nil {
		return result, err
	}
	defer op.Close()
	for _, pair := range in.Pairs {
		copied, err := op.CopyPair(ctx, pair, tctx)
		result.Effects += copied.Effects
		result.Copied = append(result.Copied, copied.Copied...)
		result.Skipped = append(result.Skipped, copied.Skipped...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// CopyPair retains operation use while the caller handles utility diagnostics.
func (op *CopyOperation) CopyPair(ctx context.Context, pair FilePair, tctx tools.ToolContext) (result CopyResult, resultErr error) {
	defer func() {
		resultErr = errors.Join(resultErr, op.settlePending(ctx, tctx))
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if op.reads == nil {
		// Mutation scope still resolves every source; only its held read root is reused.
		op.reads = projectpaths.NewReadSession(nil, tctx)
	}
	in, t := op.input, op.tool
	if in.Recursive {
		source, err := assertCopyWritePath(ctx, t.Boundary, tctx, pair.From, tctx.ProfileID())
		if err != nil {
			return result, err
		}
		if info, err := os.Lstat(source.Abs); err == nil && info.IsDir() {
			err := op.copyTree(ctx, tctx, source, pair, &result)
			return result, err
		}
	}
	limit := op.maxFileBytes
	if in.Recursive {
		op.used.Files++
		if err := copyTreeWithinCaps(op.used.Files, op.used.Bytes, op.caps, filepath.ToSlash(pair.From)); err != nil {
			return result, err
		}
		limit = min(limit, op.caps.Bytes-op.used.Bytes)
	}
	err := op.copyFile(ctx, tctx, pair, limit, &result)
	if in.Recursive && len(result.Copied) > 0 {
		op.used.Bytes += result.Copied[len(result.Copied)-1].Bytes
	}
	return result, err
}

func (op *CopyOperation) copyFile(ctx context.Context, tctx tools.ToolContext, pair FilePair, maxBytes int64, result *CopyResult) error {
	t, in := op.tool, op.input
	prof := tctx.ProfileID()
	fromSlash, toSlash := filepath.ToSlash(pair.From), filepath.ToSlash(pair.To)
	srcResolved, err := assertMutationFileSource(ctx, t.Boundary, tctx, pair.From, prof, "copy",
		"COPY_NOT_FOUND", "COPY_IS_DIRECTORY", "COPY_SYMLINK_ESCAPE", assertCopyWritePath)
	if err != nil {
		return err
	}
	dstResolved, err := assertCopyWritePath(ctx, t.Boundary, tctx, pair.To, prof)
	if err != nil {
		return err
	}
	if in.NoClobber {
		if _, err := os.Lstat(dstResolved.Abs); err == nil {
			if err := op.retainResult(ctx, pair); err != nil {
				return err
			}
			result.Skipped = append(result.Skipped, FilePair{From: fromSlash, To: toSlash})
			return nil
		}
	}
	if err := op.retainResult(ctx, pair); err != nil {
		return err
	}
	publication, err := op.copyFileWithinLimit(ctx, tctx, srcResolved, resolvedMutationTarget(dstResolved), maxBytes)
	if publication.Publication.Committed() {
		result.Effects++
		result.Copied = append(result.Copied, copyResult{From: fromSlash, To: toSlash, Bytes: publication.Bytes})
		afterSuccessfulMutation(ctx, tctx, toSlash)
	}
	if err != nil {
		reject := &toolrejection.ToolReject{}
		if errors.As(err, &reject) {
			return reject
		}
		return fmt.Errorf("copy %s -> %s: %w", pair.From, pair.To, err)
	}
	if in.Preserve {
		return preserveMetadata(ctx, tctx, srcResolved.Abs, resolvedMutationTarget(dstResolved))
	}
	return nil
}

// preserveMetadata copies permission bits and times from source to target
// through the reviewed metadata path. A platform without timestamp support
// refuses rather than leaving -p half applied silently.
func preserveMetadata(ctx context.Context, tctx tools.ToolContext, source string, target mutationTarget) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if err := chmodAgentPath(ctx, tctx, target, info.Mode().Perm()); err != nil {
		return err
	}
	modify := info.ModTime()
	var access *time.Time
	if at, ok := fseffect.AccessTime(info); ok {
		access = &at
	}
	if err := timesAgentPath(ctx, tctx, target, access, &modify); err != nil {
		if errors.Is(err, fseffect.ErrUnsupported) {
			return &toolrejection.ToolReject{Code: "COMMAND_PLATFORM_UNAVAILABLE", Data: map[string]any{"path": target.Abs, "capability": "timestamps"}}
		}
		return err
	}
	return nil
}

func (op *CopyOperation) stageCopyFileWithinLimit(ctx context.Context, tctx tools.ToolContext, source projectpaths.Resolved, target mutationTarget, maxBytes int64, copyBuffer []byte, knownAbsent bool) (*agentStreamStage, fseffect.Result, error) {
	in, err := op.reads.Open(ctx, source)
	if err != nil {
		return nil, fseffect.Result{}, err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return nil, fseffect.Result{}, err
	}
	if info.Size() > maxBytes {
		return nil, fseffect.Result{}, &toolrejection.ToolReject{
			Code: "COPY_SIZE_EXCEEDED",
			Data: map[string]any{
				"max_file_bytes": maxBytes,
				"size":           info.Size(),
			},
		}
	}
	readLimit := maxBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	mode := info.Mode().Perm()
	stage, result, err := stageAgentStream(ctx, tctx, agentStreamRequest{
		Mode: &mode, Memory: op.memory, CopyBuffer: copyBuffer, KnownAbsent: knownAbsent,
		Target: target, Source: contextio.Reader{Context: ctx, Source: io.LimitReader(in, readLimit)},
		BeforeCommit: func(_ fseffect.Target, staged fseffect.Result) error {
			if staged.Bytes <= maxBytes {
				return nil
			}
			return &toolrejection.ToolReject{
				Code: "COPY_SIZE_EXCEEDED",
				Data: map[string]any{
					"max_file_bytes": maxBytes,
					"size":           staged.Bytes,
				},
			}
		},
	})
	if err != nil {
		return nil, fseffect.Result{}, err
	}
	return stage, result, nil
}

func (op *CopyOperation) copyFileWithinLimit(ctx context.Context, tc tools.ToolContext, source projectpaths.Resolved, target mutationTarget, maximum int64) (fseffect.Result, error) {
	staged, result, err := op.stageCopyFileWithinLimit(ctx, tc, source, target, maximum, nil, false)
	if err != nil {
		return result, err
	}
	defer staged.Close()
	return staged.Commit(ctx, tc)
}

// retainResult admits result backing overlap and retained path bytes before the
// effect that will append them. Slice growth copies headers, not string backing
// bytes. The invocation owns these leases until Close.
func (op *CopyOperation) retainResult(ctx context.Context, pair FilePair) error {
	bound := int64(3*48 + len(pair.From) + len(pair.To) + 128)
	release, err := op.memory.ReserveProgramState(ctx, bound)
	if err != nil {
		return err
	}
	op.resultReleases = append(op.resultReleases, release)
	return nil
}

// Close returns retained result storage after the caller consumes the result.
func (op *CopyOperation) Close() {
	for _, pending := range op.pending {
		pending.Close()
	}
	op.pending = nil
	if op.pendingRelease != nil {
		op.pendingRelease()
		op.pendingRelease = nil
	}
	op.pendingRecords, op.pendingBytes = 0, 0
	if op.reads != nil {
		op.reads.Close()
		op.reads = nil
	}
	for _, release := range op.resultReleases {
		release()
	}
	op.resultReleases = nil
}
