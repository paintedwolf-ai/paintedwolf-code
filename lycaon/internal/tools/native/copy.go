package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// CopyTool copies regular files within write scope.
type CopyTool struct {
	Boundary *sandbox.Boundary
}

type copyResult struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Bytes int64  `json:"bytes"`
}

type copyResponse struct {
	Copied []copyResult `json:"copied"`
}

func (t *CopyTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pairs, maxBytes, err := parseCopyPairs(args)
	if err != nil {
		return "", err
	}
	prof := tctx.ProfileID()
	resp := copyResponse{Copied: make([]copyResult, 0, len(pairs))}
	defer func() {
		if runErr != nil && len(resp.Copied) > 0 {
			output, runErr = interruptedMutationBatch(resp, runErr)
		}
	}()
	for _, pair := range pairs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fromSlash := filepath.ToSlash(pair.From)
		toSlash := filepath.ToSlash(pair.To)
		srcResolved, err := assertMutationFileSource(
			ctx, t.Boundary, tctx, pair.From, prof, "copy",
			"COPY_NOT_FOUND", "COPY_IS_DIRECTORY", "COPY_SYMLINK_ESCAPE",
			assertCopyWritePath,
		)
		if err != nil {
			return "", err
		}
		dstResolved, err := assertCopyWritePath(ctx, t.Boundary, tctx, pair.To, prof)
		if err != nil {
			return "", err
		}
		n, err := copyFileWithinLimit(ctx, tctx, resolvedMutationTarget(srcResolved), resolvedMutationTarget(dstResolved), maxBytes)
		if err != nil {
			reject := &tools.ToolReject{}
			if errors.As(err, &reject) {
				return "", reject
			}
			return "", fmt.Errorf("copy %s -> %s: %w", pair.From, pair.To, err)
		}
		resp.Copied = append(resp.Copied, copyResult{
			From:  fromSlash,
			To:    toSlash,
			Bytes: n,
		})
		afterSuccessfulMutation(ctx, tctx, toSlash)
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
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
		return 0, &tools.ToolReject{
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
			return &tools.ToolReject{
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
