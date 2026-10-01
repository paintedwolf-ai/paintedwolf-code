package main

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

func replaceFile(path string, data []byte, mode os.FileMode) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(absolute),
		Source:   bytes.NewReader(data),
		Mode:     mode,
		DirMode:  0o755,
	})
	return err
}
