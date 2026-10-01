package extpacks

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// writeCacheMetadataFile commits metadata for bytes already on disk.
func writeCacheMetadataFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}
