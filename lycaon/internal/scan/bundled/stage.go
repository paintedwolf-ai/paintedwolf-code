package bundled

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

const maxArtifactBytes = 512 << 20

// StageArtifact installs or repairs payloads from a verified artifact.
func StageArtifact(ctx context.Context, m *Manifest, root, goos, goarch string) (string, error) {
	art, err := m.ArtifactForPlatform(goos, goarch)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := verifyStageDirectories(root, m.OpenGrep.Version); err != nil {
		return "", err
	}
	if m.candidate != nil {
		if err := m.candidate.verify(); err != nil {
			return "", err
		}
		path := ArtifactPath(root, m.OpenGrep.Version, goos)
		if _, err := os.Lstat(path); err == nil {
			return path, verifyArtifactFile(path, art.SHA256, art.BinaryBytes, true, goos)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return stageCandidate(m.candidate, root, path)
	}
	if m.artifactDirectory == "" {
		return "", fmt.Errorf("staging requires a verified local artifact directory")
	}
	if err := verifyArtifactDirectory(m, m.artifactDirectory); err != nil {
		return "", err
	}
	directory := EngineBundledDir(root, m.OpenGrep.Version)
	files := append([]PayloadIdentity{}, m.identity.Payload...)
	files = append(files, PayloadIdentity{Name: filepath.Base(ArtifactPath(root, m.OpenGrep.Version, goos)), SHA256: art.SHA256, Bytes: art.BinaryBytes})
	for _, part := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		path := filepath.Join(directory, part.Name)
		executable := part.Name == filepath.Base(ArtifactPath(root, m.OpenGrep.Version, goos))
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("staged artifact must be a regular file: %s", path)
			}
			if err := verifyArtifactFile(path, part.SHA256, part.Bytes, executable, goos); err == nil {
				continue
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		mode := os.FileMode(0o644)
		if executable {
			mode = 0o755
		}
		if err := copyArtifactFile(root, path, filepath.Join(m.artifactDirectory, part.Name), part, mode); err != nil {
			return "", err
		}
	}
	return VerifyStagedArtifact(m, root, goos, goarch)
}

func copyArtifactFile(root, path, sourcePath string, part PayloadIdentity, mode os.FileMode) error {
	// #nosec G304 -- fixed artifact payload name verified before staging.
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	_, err = installArtifactFile(root, path, part.SHA256, part.Bytes, mode, source)
	return err
}

func stageCandidate(candidate *localCandidate, root, path string) (string, error) {
	// #nosec G304 -- the selected candidate was verified before staging.
	source, err := os.Open(candidate.binaryPath())
	if err != nil {
		return "", err
	}
	defer func() { _ = source.Close() }()
	return installArtifactFile(root, path, candidate.provenance.BinarySHA256, candidate.provenance.BinaryBytes, 0o755, source)
}

func installArtifactFile(root, path, digest string, size int64, mode os.FileMode, source io.Reader) (string, error) {
	// #nosec G301 -- application resources are readable across user accounts.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: rel},
		Source:   io.LimitReader(source, maxArtifactBytes+1),
		Mode:     mode,
		BeforeCommit: func(_ fseffect.Target, result fseffect.Result) error {
			if result.Bytes > maxArtifactBytes || result.Bytes != size {
				return fmt.Errorf("opengrep artifact exceeds size limit")
			}
			if result.SHA256 != digest {
				return fmt.Errorf("%w: artifact differs from manifest", ErrOpenGrepChecksum)
			}
			return nil
		},
	})
	if err != nil {
		return "", err
	}
	if err := VerifyFileSHA256(path, digest); err != nil {
		return "", err
	}
	return path, nil
}

// ArtifactPath selects the declared target even when staging from a different OS.
func ArtifactPath(root, version, goos string) string {
	name := "opengrep"
	if goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(EngineBundledDir(root, version), name)
}

func PlatformForTarget(target string) (string, string, error) {
	switch target {
	case "aarch64-apple-darwin":
		return "darwin", "arm64", nil
	case "x86_64-apple-darwin":
		return "darwin", "amd64", nil
	case "aarch64-unknown-linux-gnu":
		return "linux", "arm64", nil
	case "x86_64-unknown-linux-gnu":
		return "linux", "amd64", nil
	case "x86_64-pc-windows-msvc":
		return "windows", "amd64", nil
	default:
		return "", "", fmt.Errorf("unsupported bundle target: %s", target)
	}
}

func verifyStageDirectories(root, version string) error {
	for _, path := range []string{filepath.Join(root, "bundled"), EngineBundledDir(root, version)} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("staged artifact parent must be a directory: %s", path)
		}
	}
	return nil
}
