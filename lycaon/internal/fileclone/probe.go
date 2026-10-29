package fileclone

import (
	"os"

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
func SupportedFrom(source, root string) (bool, error) {
	placeholder, err := os.CreateTemp(root, ".clone-source-probe-")
	if err != nil {
		return false, err
	}
	path := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(path)
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	cloned, err := Clone(source, path)
	if cloned {
		defer func() { _ = os.Remove(path) }()
	}
	return cloned, err
}
