package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type appendLanding struct {
	bytes           int
	publishedSHA256 string
}

// landPinnedAppend copies the held original followed by the suffix through
// the existing stream door. Both the copied prefix and target must still name
// the original captured bytes; no approval or source pin is refreshed.
func landPinnedAppend(ctx context.Context, tc tools.ToolContext, resolved projectpaths.Resolved, pin toolcontract.ExecutionRevision, suffix []byte, admitSources func() error) (landing appendLanding, finalErr error) {
	file, err := fseffect.OpenRead(resolved.EffectLocation())
	if err != nil {
		return appendLanding{}, err
	}
	defer func() {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				if finalErr == nil {
					finalErr = closeErr
				} else {
					finalErr = errors.Join(finalErr, closeErr)
				}
			}
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return appendLanding{}, err
	}
	prefixHash := sha256.New()
	prefix := io.TeeReader(io.NewSectionReader(file, 0, info.Size()), prefixHash)
	result, err := applyAgentStream(ctx, tc, agentStreamRequest{Target: resolvedMutationTarget(resolved), Source: io.MultiReader(prefix, bytes.NewReader(suffix)), BeforeCommit: func(current fseffect.Target, _ fseffect.Result) error {
		if hex.EncodeToString(prefixHash.Sum(nil)) != pin.DiskSHA256 {
			return &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: pin}
		}
		if err := verifyRawBase(current, resolved.DisplayPath, pin.DiskSHA256); err != nil {
			return &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: pin, Cause: err}
		}
		// Finish the original read before publication, so a close failure
		// cannot turn a completed write into an unreported write failure.
		if file != nil {
			closeErr := file.Close()
			file = nil
			if closeErr != nil {
				return closeErr
			}
		}
		return admitSources()
	}})
	if err != nil {
		return appendLanding{}, err
	}
	return appendLanding{bytes: int(info.Size()) + len(suffix), publishedSHA256: result.SHA256}, nil
}
