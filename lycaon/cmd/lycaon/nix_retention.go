package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
)

// The desktop owns stdin until shutdown. EOF also releases the root when the
// desktop crashes, while the next launch reclaims roots from a killed retainer.
func runNixRetainer(ctx context.Context, lockPath string, stdin io.Reader, stdout io.Writer) error {
	if !filepath.IsAbs(lockPath) || filepath.Clean(lockPath) != lockPath {
		return errors.New("Nix retention requires its registered session lock")
	}
	if err := json.NewEncoder(stdout).Encode(struct {
		FormatVersion int    `json:"format_version"`
		LockPath      string `json:"lock_path"`
	}{1, lockPath}); err != nil {
		return err
	}
	closed := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stdin); close(closed) }()
	select {
	case <-closed:
		return nil
	case <-ctx.Done():
		return nil
	}
}
