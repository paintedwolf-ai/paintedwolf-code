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
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
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
	tool         *CopyTool
	input        CopyInput
	caps, used   RecursiveCaps
	maxFileBytes int64
}

func (t *CopyTool) Begin(in CopyInput) (*CopyOperation, error) {
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
	return &CopyOperation{tool: t, input: in, caps: caps, maxFileBytes: maxBytes}, nil
}

// Copy writes a complete batch through one operation's mutation streams.
func (t *CopyTool) Copy(ctx context.Context, in CopyInput, tctx tools.ToolContext) (CopyResult, error) {
	var result CopyResult
	op, err := t.Begin(in)
	if err != nil {
		return result, err
	}
	for _, pair := range in.Pairs {
		copied, err := op.CopyPair(ctx, pair, tctx)
		result.Copied = append(result.Copied, copied.Copied...)
		result.Skipped = append(result.Skipped, copied.Skipped...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// CopyPair retains operation use while the caller handles utility diagnostics.
func (op *CopyOperation) CopyPair(ctx context.Context, pair FilePair, tctx tools.ToolContext) (CopyResult, error) {
	var result CopyResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	in, t := op.input, op.tool
	if in.Recursive {
		source, err := assertCopyWritePath(ctx, t.Boundary, tctx, pair.From, tctx.ProfileID())
		if err != nil {
			return result, err
		}
		if info, err := os.Lstat(source.Abs); err == nil && info.IsDir() {
			err := t.copyTree(ctx, tctx, source, pair, in, op.caps, &op.used, &result)
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
	err := t.copyFile(ctx, tctx, pair, in, limit, &result)
	if in.Recursive && len(result.Copied) > 0 {
		op.used.Bytes += result.Copied[len(result.Copied)-1].Bytes
	}
	return result, err
}

func (t *CopyTool) copyFile(ctx context.Context, tctx tools.ToolContext, pair FilePair, in CopyInput, maxBytes int64, result *CopyResult) error {
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
			result.Skipped = append(result.Skipped, FilePair{From: fromSlash, To: toSlash})
			return nil
		}
	}
	n, err := copyFileWithinLimit(ctx, tctx, resolvedMutationTarget(srcResolved), resolvedMutationTarget(dstResolved), maxBytes)
	if err != nil {
		reject := &toolrejection.ToolReject{}
		if errors.As(err, &reject) {
			return reject
		}
		return fmt.Errorf("copy %s -> %s: %w", pair.From, pair.To, err)
	}
	result.Copied = append(result.Copied, copyResult{From: fromSlash, To: toSlash, Bytes: n})
	afterSuccessfulMutation(ctx, tctx, toSlash)
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

func copyFileWithinLimit(ctx context.Context, tctx tools.ToolContext, source, target mutationTarget, maxBytes int64) (int64, error) {
	in, err := fseffect.OpenRead(source.Location)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() > maxBytes {
		return 0, &toolrejection.ToolReject{
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
	result, err := applyAgentStream(ctx, tctx, agentStreamRequest{
		Mode:   &mode,
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
		return 0, err
	}
	return result.Bytes, nil
}
