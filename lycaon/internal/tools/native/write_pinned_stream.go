package native

import (
	"context"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// applyPinnedStream lands large raw output without materializing it. The caller
// validated the pinned admission; document writes retain the bounded text door.
func (t *WriteTool) applyPinnedStream(ctx context.Context, input WriteInput, tc tools.ToolContext, resolved projectpaths.Resolved, revisions ExecutionRevisions) (WriteResult, error) {
	st, current, err := captureDestinationRevision(ctx, tc, resolved)
	if err != nil {
		return WriteResult{}, err
	}
	if !current.Same(input.Destination) {
		return WriteResult{}, &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: input.Destination, Current: current}
	}
	if st.Editor != nil {
		return WriteResult{}, sourceview.MutationSizeReject("write", resolved.DisplayPath, input.SourceBytes)
	}
	admitSources := func() error { return revisions.Verify(ctx, tc, input.Sources...) }
	if err := admitSources(); err != nil {
		return WriteResult{}, err
	}
	var digest string
	resultBytes := input.SourceBytes
	if input.Append && current.Mode != toolcontract.AbsentRevision {
		landed, err := landPinnedAppend(ctx, tc, resolved, input.Destination, input.Source, input.SourceBytes, admitSources)
		if err != nil {
			return WriteResult{}, err
		}
		digest, resultBytes = landed.publishedSHA256, int64(landed.bytes)
	} else {
		result, err := applyAgentStream(ctx, tc, agentStreamRequest{Target: resolvedMutationTarget(resolved), Source: io.LimitReader(input.Source, input.SourceBytes+1), Mode: newFileMode(input.Mode),
			BeforeCommit: func(current fseffect.Target, staged fseffect.Result) error {
				if staged.Bytes != input.SourceBytes {
					return errors.New("exact write stream length changed")
				}
				if err := verifyRawBase(current, resolved.DisplayPath, input.Destination.DiskSHA256); err != nil {
					return &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: input.Destination, Cause: err}
				}
				return admitSources()
			}})
		if err != nil {
			return WriteResult{}, err
		}
		digest = result.SHA256
	}
	afterSuccessfulMutation(ctx, tc, resolved.DisplayPath)
	return WriteResult{Path: resolved.DisplayPath, Appended: input.Append, InputBytes: int(input.SourceBytes), ResultBytes: int(resultBytes),
		Accepted: true, Saved: true, PublishedSHA256: digest}, nil
}
