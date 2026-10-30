package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
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

// Copy writes each destination through the reviewed, recoverable stream door.
func (t *CopyTool) Copy(ctx context.Context, in CopyInput, tctx tools.ToolContext) (CopyResult, error) {
	var result CopyResult
	maxBytes := in.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = defaultCopyMaxBytes
	}
	prof := tctx.ProfileID()
	for _, pair := range in.Pairs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if in.Recursive {
			source, err := assertCopyWritePath(ctx, t.Boundary, tctx, pair.From, prof)
			if err != nil {
				return result, err
			}
			if info, err := os.Lstat(source.Abs); err == nil && info.IsDir() {
				if err := t.copyTree(ctx, tctx, source.Abs, pair, in, maxBytes, &result); err != nil {
					return result, err
				}
				continue
			}
		}
		if err := t.copyFile(ctx, tctx, pair, in, maxBytes, &result); err != nil {
			return result, err
		}
	}
	return result, nil
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

// copyTree recreates a directory tree at the destination, parents first.
func (t *CopyTool) copyTree(ctx context.Context, tctx tools.ToolContext, sourceRoot string, pair FilePair, in CopyInput, maxBytes int64, result *CopyResult) error {
	type entry struct {
		path  string
		isDir bool
	}
	var order []entry
	if err := walkRecursiveTree(t.Caps, sourceRoot, filepath.ToSlash(pair.From), func(path string, isDir bool) error {
		order = append(order, entry{path, isDir})
		return nil
	}); err != nil {
		return err
	}
	prof := tctx.ProfileID()
	for i := len(order) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(sourceRoot, order[i].path)
		if err != nil {
			return err
		}
		to := filepath.Join(pair.To, rel)
		if !order[i].isDir {
			if err := t.copyFile(ctx, tctx, FilePair{From: order[i].path, To: to}, in, maxBytes, result); err != nil {
				return err
			}
			continue
		}
		target, err := assertCopyWritePath(ctx, t.Boundary, tctx, to, prof)
		if err != nil {
			return err
		}
		if err := mkdirAgentPath(ctx, tctx, resolvedMutationTarget(target), 0o755); err != nil {
			return fmt.Errorf("copy %s: %w", filepath.ToSlash(to), err)
		}
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
	result, err := applyAgentStream(ctx, tctx, agentStreamRequest{
		Target: target, Source: io.LimitReader(in, readLimit),
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
