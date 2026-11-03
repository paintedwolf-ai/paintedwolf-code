package fileclone

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fssync"
)

// SupportedAt probes the actual volume without cloning installation data.
func SupportedAt(root string) (supported bool, err error) {
	directory, err := os.MkdirTemp(root, ".clone-volume-probe-")
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	path := filepath.Join(directory, "source")
	source, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	probe := make([]byte, 4096)
	probe[0] = 1
	if _, err := source.Write(probe); err != nil {
		return false, errors.Join(err, source.Close())
	}
	if err := fssync.File(source); err != nil {
		return false, errors.Join(err, source.Close())
	}
	if err := source.Close(); err != nil {
		return false, err
	}
	return Clone(path, filepath.Join(directory, "clone"))
}

// SupportedFrom tests this source file and destination volume. It never falls
// back to a streamed copy, so large sources cost only clone metadata here.
func SupportedFrom(ctx context.Context, source, root string) (bool, error) {
	return probeSourceClone(ctx, source, root, Clone, os.RemoveAll)
}

// The lifetime belongs here even when the platform leaves a partial result.
// Dependencies bind the filesystem boundary; cleanup never inherits cancellation.
func probeSourceClone(ctx context.Context, source, root string, clone func(string, string) (bool, error), remove func(string) error) (supported bool, err error) {
	if err := context.Cause(ctx); err != nil {
		return false, err
	}
	directory, err := os.MkdirTemp(root, ".clone-source-probe-")
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, remove(directory)) }()
	supported, err = clone(source, filepath.Join(directory, "probe"))
	return supported, errors.Join(err, context.Cause(ctx))
}
