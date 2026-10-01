package backup

import (
	"context"
	"math"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/editoroutbox"
)

// Outbox capture precedes the database snapshot so later deliveries remain replayable.
// Cloning releases editing before database capture and compression.
func captureEditorOutbox(ctx context.Context, source, destination string) error {
	release, err := editoroutbox.Acquire(ctx, source)
	if err != nil {
		return err
	}
	defer release()
	files := make(map[string]archiveSource)
	if err := collectDurableDir(ctx, source, editoroutbox.Directory(), files, branchCapture{all: true}, math.MaxInt); err != nil {
		return err
	}
	for relative, file := range files {
		if err := copySnapshotSource(ctx, file, filepath.Join(destination, filepath.FromSlash(relative))); err != nil {
			return err
		}
	}
	return nil
}
