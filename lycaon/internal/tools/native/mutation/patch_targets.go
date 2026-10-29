package mutation

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// PatchTarget is one file a patch section reads and writes, captured before
// any patch effect: the bytes its hunks apply to and the revisions its
// publication must still find. patch and git apply share it, so both stage
// every section first and publish each file only at its captured revision.
type PatchTarget struct {
	// OldAbs is the file the hunks read, NewAbs the file they produce;
	// either is "" when the section creates or deletes.
	OldAbs, NewAbs string
	// Exists reports that OldAbs was present and Before holds its bytes.
	Exists bool
	Before []byte
	// NewExists reports that NewAbs was present when it was pinned.
	NewExists bool

	source      toolcontract.ExecutionRevision
	destination toolcontract.ExecutionRevision
}

// ErrPatchTargetMissing reports that the file a section reads is absent.
var ErrPatchTargetMissing = errors.New("no such file")

// CapturePatchTarget pins the section's input bytes and its destination
// before anything is computed or written. Each capture is recorded against
// the execution. A missing input returns ErrPatchTargetMissing joined with
// the owner's not-found refusal; authority refusals stay typed.
func (o *Owners) CapturePatchTarget(ctx context.Context, tc tools.ToolContext, executionID, oldAbs, newAbs string) (PatchTarget, error) {
	target := PatchTarget{OldAbs: oldAbs, NewAbs: newAbs}
	if oldAbs != "" {
		captured, err := o.Revisions.CaptureBytes(ctx, tc, oldAbs, o.limit("stream_bytes"))
		if err != nil {
			if _, ok := ordinary(err); ok {
				return target, errors.Join(ErrPatchTargetMissing, err)
			}
			return target, err
		}
		defer captured.Close()
		if err := o.Pins.AppendRevisionPin(ctx, executionID, captured.Revision); err != nil {
			return target, err
		}
		target.Exists, target.Before, target.source = true, append([]byte(nil), captured.Bytes...), captured.Revision
	}
	checked := map[string]bool{"": true}
	for _, abs := range []string{oldAbs, newAbs} {
		if checked[abs] {
			continue
		}
		checked[abs] = true
		if err := o.Revisions.CheckUnsavedDraft(ctx, tc, abs, false); err != nil {
			return target, err
		}
	}
	if newAbs != "" {
		pinned, err := o.captureDestination(ctx, tc, executionID, newAbs)
		if err != nil {
			return target, err
		}
		target.destination = pinned.pin
		target.NewExists = pinned.pin.Mode != toolcontract.AbsentRevision
	}
	return target, nil
}

// CapturePatchInput reads a patch file as a pinned source of the execution.
func (o *Owners) CapturePatchInput(ctx context.Context, tc tools.ToolContext, executionID, abs string) ([]byte, error) {
	captured, err := o.Revisions.CaptureBytes(ctx, tc, abs, o.limit("stream_bytes"))
	if err != nil {
		return nil, err
	}
	defer captured.Close()
	if err := o.Pins.AppendRevisionPin(ctx, executionID, captured.Revision); err != nil {
		return nil, err
	}
	return append([]byte(nil), captured.Bytes...), nil
}

// PatchResult is how one captured target changes.
type PatchResult struct {
	// After is written to NewAbs when Write is set.
	After []byte
	Write bool
	// RemoveOld deletes OldAbs: a deletion, or the old side of a rename.
	RemoveOld bool
	// Mode, "644" or "755", is applied to NewAbs after the write.
	Mode string
}

// PatchEffect is what publishing one target changed.
type PatchEffect struct {
	Wrote, Removed bool
}

// PublishPatchTarget lands one section's result through the pinned write
// door and the delete owner. Removal first rechecks the captured input. A
// file changed since capture is COMMAND_REVISION_CONFLICT and nothing lands
// for it.
func (o *Owners) PublishPatchTarget(ctx context.Context, tc tools.ToolContext, target PatchTarget, change PatchResult) (PatchEffect, error) {
	var effect PatchEffect
	if change.Write {
		var sources []toolcontract.ExecutionRevision
		if target.Exists {
			sources = []toolcontract.ExecutionRevision{target.source}
		}
		result, err := o.Operations.Write.Apply(ctx, native.WriteInput{Path: target.NewAbs, Exact: true, Bytes: change.After, Memory: o.Revisions.Memory,
			Admission: native.WriteExecutionPinned, Destination: target.destination, Sources: sources}, tc)
		if err != nil {
			return effect, err
		}
		effect.Wrote = result.Accepted || result.Saved
		if change.Mode != "" {
			if _, err := o.Operations.Chmod.Chmod(ctx, []string{target.NewAbs}, change.Mode, tc); err != nil {
				return effect, err
			}
		}
	}
	if change.RemoveOld && target.Exists {
		if err := o.Revisions.Verify(ctx, tc, target.source); err != nil {
			return effect, err
		}
		deleted, err := o.Operations.Delete.Delete(ctx, native.DeleteInput{Memory: o.Revisions.Memory, Paths: []string{target.OldAbs}, FilesOnly: true}, tc)
		if err != nil {
			return effect, err
		}
		effect.Removed = len(deleted.Deleted) > 0
	}
	return effect, nil
}
