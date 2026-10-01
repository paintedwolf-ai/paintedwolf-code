package blobstore

import (
	"bytes"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

func touchRetentionMarker(root, blobID, retentionID string) error {
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: filepath.Join(retentionDir, blobID, retentionID)},
		Source:   bytes.NewReader([]byte{0}),
		Mode:     0o600,
		DirMode:  0o700,
	}); err != nil {
		return fmt.Errorf("blobstore: refresh retention marker: %w", err)
	}
	return nil
}
