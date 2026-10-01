package extpacks

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/filelock"
)

const extensionPathLockTimeout = 5 * time.Second

// lockFilePathFor keeps advisory locks outside managed content.
func lockFilePathFor(target string) (string, error) {
	root, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		abs = target
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	dir := filepath.Join(root, "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".lock"), nil
}

func lockExtensionPathAcrossProcesses(target string) (func(), error) {
	path, err := lockFilePathFor(target)
	if err != nil {
		return nil, err
	}
	f, err := filelock.Open(path)
	if err != nil {
		return nil, fmt.Errorf("extension lock: %w", err)
	}
	deadline := time.Now().Add(extensionPathLockTimeout)
	for {
		ok, err := filelock.TryExclusive(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("extension lock: %w", err)
		}
		if ok {
			return func() {
				_ = filelock.Unlock(f)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("extension lock: %s is held by another process; retry", filepath.Base(target))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
