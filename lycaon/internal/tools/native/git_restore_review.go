package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

func (t *GitRestoreTool) reviewRestore(ctx context.Context, tc tools.ToolContext, files []git.RestoreFile) error {
	return reviewGitFiles(ctx, tc, t.Boundary, "git_restore", files)
}

func reviewGitFiles(ctx context.Context, tc tools.ToolContext, boundary *sandbox.Boundary, tool string, files []git.RestoreFile) error {
	changes := make([]tools.FileChange, 0, len(files))
	for _, file := range files {
		absolute := filepath.Join(tools.HostWriteRoot(tc), file.Path)
		if err := assertGitOperationPath(ctx, boundary, tc, absolute, tool); err != nil {
			return err
		}
		resolved, err := projectpaths.ResolveWrite(ctx, boundary, tc, absolute)
		if err != nil {
			return err
		}
		op := api.SourceChangeOpWrite
		if !file.Before.Exists {
			op = api.SourceChangeOpCreate
		}
		if !file.After.Exists {
			op = api.SourceChangeOpDelete
		}
		mutation := agentMutation{Op: op, AbsPath: resolved.Abs, Before: file.Before.Bytes, After: file.After.Bytes,
			BeforeSize: file.Before.Size, AfterSize: file.After.Size, BeforeSHA256: file.Before.SHA256, AfterSHA256: file.After.SHA256}
		preview := agentMutationPreview(tc, mutation, resolved.EffectLocation(), fseffect.Location{})
		switch {
		case file.After.Mode&os.ModeSymlink != 0:
			preview.Operation = "restore symbolic link"
		case file.Before.Mode&os.ModeSymlink != 0 && file.After.Exists:
			preview.Operation = "replace symbolic link with file"
		case file.Before.Mode&os.ModeSymlink != 0:
			preview.Operation = "delete symbolic link"
		}
		if file.IndexOnly {
			preview.Target = "index"
			if file.IndexStage > 0 {
				preview.PreviewNote = fmt.Sprintf("Conflict stage %d changes.", file.IndexStage)
			}
		}
		if file.Before.SHA256 == file.After.SHA256 && file.Before.Mode != file.After.Mode {
			preview.PreviewNote = fmt.Sprintf("File mode changes from %s to %s.", file.Before.Mode, file.After.Mode)
		}
		changes = append(changes, tools.FileChange{Path: absolute, Preview: preview})
	}
	return tc.ReviewFileChanges(ctx, changes...)
}
