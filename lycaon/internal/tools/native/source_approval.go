package native

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func reviewAgentMutation(ctx context.Context, tc tools.ToolContext, m agentMutation, target, from fseffect.Location, metadata bool) error {
	preview := agentMutationPreview(tc, m, target, from)
	if metadata {
		preview.PreviewNote = m.MetadataChange
	}
	checkProgress := func() error {
		if tc.Effects.CheckMutationProgress == nil || preview.RootID == "" || m.Op == api.SourceChangeOpDelete {
			return nil
		}
		return tc.Effects.CheckMutationProgress(ctx, preview.RootID, m.AbsPath)
	}
	if err := checkProgress(); err != nil {
		return err
	}
	if err := tc.ReviewFileChanges(ctx, tools.FileChange{Preview: preview, Path: m.AbsPath, FromPath: m.FromAbsPath, RecoveryLoss: m.Unrecoverable}); err != nil {
		return err
	}
	if err := checkProgress(); err != nil {
		return err
	}
	verifyTarget := verifyMutationSnapshot
	if m.Op == api.SourceChangeOpDelete || m.Op == api.SourceChangeOpRename || m.IsLink {
		// A removed or replaced entry is the entry itself, never a link's target.
		verifyTarget = verifyEntrySnapshot
	}
	if err := verifyTarget(ctx, target, m.BeforeSHA256, m.IsDir); err != nil {
		return err
	}
	if m.FromAbsPath != "" {
		return verifyMutationSnapshot(ctx, from, m.AfterSHA256, m.IsDir)
	}
	return nil
}

func reviewHeldAgentMutation(ctx context.Context, tc tools.ToolContext, m agentMutation, target fseffect.Location, held fseffect.Target) error {
	preview := agentMutationPreview(tc, m, target, fseffect.Location{})
	checkProgress := func() error {
		if tc.Effects.CheckMutationProgress == nil || preview.RootID == "" {
			return nil
		}
		return tc.Effects.CheckMutationProgress(ctx, preview.RootID, m.AbsPath)
	}
	if err := checkProgress(); err != nil {
		return err
	}
	if err := tc.ReviewFileChanges(ctx, tools.FileChange{Preview: preview, Path: m.AbsPath, FromPath: m.FromAbsPath, RecoveryLoss: m.Unrecoverable}); err != nil {
		return err
	}
	if err := checkProgress(); err != nil {
		return err
	}
	return verifyHeldMutationSnapshot(ctx, target, held, m.BeforeSHA256)
}

func verifyMutationSnapshot(ctx context.Context, location fseffect.Location, expected string, directory bool) error {
	current, err := captureAgentFileAt(location, ctx, nil, false)
	return snapshotMatches(location, current, err, expected, directory)
}

// The staged replacement has already revalidated its reviewed parent before
// this check. Read the actual target beneath that held descriptor.
func verifyHeldMutationSnapshot(ctx context.Context, location fseffect.Location, target fseffect.Target, expected string) error {
	file, err := target.Open()
	var current agentFileEvidence
	if err == nil {
		current, err = captureAgentFile(ctx, file, nil, false)
	} else if os.IsNotExist(err) {
		err = nil
	}
	return snapshotMatches(location, current, err, expected, false)
}

func verifyEntrySnapshot(ctx context.Context, location fseffect.Location, expected string, directory bool) error {
	current, err := captureRemovedEntry(ctx, location, nil, false)
	return snapshotMatches(location, current, err, expected, directory)
}

// errMutationSnapshotMoved marks a target that changed while its mutation was
// prepared or reviewed.
var errMutationSnapshotMoved = errors.New("file changed while preparing or reviewing the change")

func snapshotMatches(location fseffect.Location, current agentFileEvidence, err error, expected string, directory bool) error {
	if err != nil {
		return err
	}
	if current.sha256 != expected || (current.exists && current.isDir != directory) {
		return fmt.Errorf("%w: %s", errMutationSnapshotMoved, location.Rel)
	}
	return nil
}

func agentMutationPreview(tc tools.ToolContext, m agentMutation, target, from fseffect.Location) api.ApprovalFileChange {
	p := api.ApprovalFileChange{
		Path: m.AbsPath, FromPath: m.FromAbsPath, Operation: string(m.Op),
		BeforeSHA256: m.BeforeSHA256, AfterSHA256: m.AfterSHA256,
		BeforeBytes: max(m.BeforeSize, int64(len(m.Before))), AfterBytes: max(m.AfterSize, int64(len(m.After))),
	}
	for _, root := range tc.Source.Roots {
		if root.Path == target.Root {
			p.RootID, p.Path = root.ID, target.Rel
		}
		if m.FromAbsPath != "" && root.Path == from.Root {
			p.FromPath = from.Rel
		}
	}
	if m.IsDir {
		if m.MetadataChange != "" {
			p.PreviewNote = m.MetadataChange
			return p
		}
		p.PreviewNote = "This changes a directory."
		return p
	}
	var beforeNote, afterNote string
	p.Before, beforeNote = approvalText(m.Before, p.BeforeBytes)
	p.After, afterNote = approvalText(m.After, p.AfterBytes)
	// The review carries the reference the call named, never the value it resolves.
	p.Before, p.After = tc.Effects.Secrets.ReferenceEchoes(p.Before), tc.Effects.Secrets.ReferenceEchoes(p.After)
	if beforeNote != "" {
		p.PreviewNote = beforeNote
	}
	if afterNote != "" {
		p.PreviewNote = afterNote
	}
	if p.PreviewNote == "" && m.BeforeSHA256 != "" && m.AfterSHA256 != "" && m.BeforeSHA256 != m.AfterSHA256 && p.Before == p.After {
		p.PreviewNote = "Encoded file bytes change; decoded text is unchanged."
	}
	return p
}

func approvalText(raw []byte, size int64) (string, string) {
	if size > sourceledger.MaxRevisionContentBytes {
		return "", "This file is too large for a text preview. The change identifies its size and content hash."
	}
	if size > int64(len(raw)) {
		return "", "Text preview omitted to keep this review within its content limit. The change identifies its size and content hash."
	}
	if len(raw) == 0 {
		return "", ""
	}
	text, _, err := textfile.Decode(raw, textfile.LimitsForRaw(sourceledger.MaxRevisionContentBytes))
	if err != nil {
		return "", "This file has no text preview. The change identifies its size and content hash."
	}
	return text, ""
}
