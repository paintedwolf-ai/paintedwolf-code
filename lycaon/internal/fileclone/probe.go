package fileclone

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fssync"
)

// SupportedAt probes the actual volume without cloning installation data.
func SupportedAt(root string) (bool, error) {
	source, err := os.CreateTemp(root, ".clone-probe-")
	if err != nil {
		return false, err
	}
	path := source.Name()
	defer func() { _ = os.Remove(path); _ = os.Remove(path + ".clone") }()
	probe := make([]byte, 4096)
	probe[0] = 1
	if _, err := source.Write(probe); err != nil {
		_ = source.Close()
		return false, err
	}
	if err := fssync.File(source); err != nil {
		_ = source.Close()
		return false, err
	}
	if err := source.Close(); err != nil {
		return false, err
	}
	return Clone(path, path+".clone")
}

// SupportedFrom tests this source file and destination volume. It never falls
// back to a streamed copy, so large sources cost only clone metadata here.
func SupportedFrom(source, root string) (supported bool, err error) {
	directory, err := os.MkdirTemp(root, ".clone-source-probe-")
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	return Clone(source, filepath.Join(directory, "probe"))
}
