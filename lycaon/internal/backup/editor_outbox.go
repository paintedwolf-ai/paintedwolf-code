package backup

import (
	"context"
	"math"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/editoroutbox"
)

// Outbox capture precedes the database snapshot so later deliveries remain replayable.
// Cloning releases editing before database capture and compression.
func captureEditorOutbox(ctx context.Context, source, destination string, usage *RecoveryCaptureUsage) error {
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
		destinationPath := filepath.Join(destination, filepath.FromSlash(relative))
		shared, err := copySnapshotSource(ctx, file, destinationPath)
		if err != nil {
			return err
		}
		if err := usage.addFile(destinationPath, shared); err != nil {
			return err
		}
	}
	return nil
}
