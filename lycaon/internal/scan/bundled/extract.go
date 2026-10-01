package bundled

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrOpenGrepNotFound = errors.New("opengrep binary not found")
	ErrOpenGrepChecksum = errors.New("bundled opengrep checksum failed")
)

// ResolveOpenGrepBinary checks only the exact app and device-cache artifacts.
func ResolveOpenGrepBinary(m *Manifest, homeDir, stageRoot string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("bundled manifest required")
	}
	if err := ValidateManifest(m); err != nil {
		return "", err
	}
	if m.candidate != nil {
		if err := m.candidate.verify(); err != nil {
			return "", err
		}
		return m.candidate.binaryPath(), nil
	}
	art, err := m.ArtifactForCurrentPlatform()
	if err != nil {
		return "", err
	}

	roots := make([]string, 0, 2)
	if root := strings.TrimSpace(stageRoot); root != "" {
		roots = append(roots, root)
	}
	if home := strings.TrimSpace(homeDir); home != "" {
		roots = append(roots, home)
	}
	for _, root := range roots {
		if err := verifyStageDirectories(root, m.OpenGrep.Version); err != nil {
			return "", fmt.Errorf("%w: %w", ErrOpenGrepChecksum, err)
		}
		candidate := BinaryPath(EngineBundledDir(root, m.OpenGrep.Version))
		if _, err := os.Stat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		if err := verifyArtifactDirectory(m, filepath.Dir(candidate)); err != nil {
			return "", fmt.Errorf("%w: %w", ErrOpenGrepChecksum, err)
		}
		return candidate, nil
	}

	return "", fmt.Errorf("%w for %s/%s (expected sha256 %s)", ErrOpenGrepNotFound, art.GOOS, art.GOARCH, art.SHA256)
}
