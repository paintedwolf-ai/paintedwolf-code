//go:build linux && protected_desktop_artifact

package launchguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

// Only the fixture's entry and bootstrap are linked for its private policy.
// All desktop/engine/resource bytes remain the explicit artifact's bytes.
func installDesktopGeneration(dir string, artifact desktopArtifact, input *pkgmanifest.Manifest, bootstrap, entry string) error {
	generation := filepath.Join(dir, "generation")
	install := filepath.Join(dir, "install")
	for _, path := range []string{generation, install, filepath.Join(dir, "generations")} {
		if err := os.Mkdir(path, 0o755); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(artifact.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	payload := slices.Clone(input.Payload)
	slices.SortFunc(payload, func(a, b pkgmanifest.PayloadEntry) int { return strings.Compare(a.Path, b.Path) })
	for _, row := range payload {
		target := filepath.Join(generation, row.Path)
		switch row.Kind {
		case "directory":
			err = os.Mkdir(target, os.FileMode(row.Mode))
		case "symlink":
			err = os.Symlink(*row.Target, target)
		case "file":
			err = copyArtifactFile(root, row, target)
		}
		if err != nil {
			return err
		}
	}
	entryPath := "bin/pw-launch-entry"
	for i, row := range payload {
		if row.Path == entryPath {
			payload = append(payload[:i], payload[i+1:]...)
			if err := os.Remove(filepath.Join(generation, entryPath)); err != nil {
				return err
			}
			break
		}
	}
	digest, size, err := copyRootBinary(entry, filepath.Join(generation, entryPath), 0o755, nil)
	if err != nil {
		return err
	}
	payload = append(payload, pkgmanifest.PayloadEntry{Path: entryPath, Kind: "file", Mode: 0o755, SHA256: &digest, Size: &size})
	manifest := pkgmanifest.Manifest{FormatVersion: pkgmanifest.FormatVersion, Package: input.Package, Version: input.Version, Architecture: input.Architecture, InstallRoot: install, Payload: payload}
	for _, row := range payload {
		if row.Kind == "file" && row.Mode&0o111 != 0 {
			manifest.Executables = append(manifest.Executables, pkgmanifest.Executable{Path: filepath.Join(install, row.Path), SHA256: *row.SHA256, Size: *row.Size})
		}
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	ownedRoot, err := os.OpenRoot(generation)
	if err != nil {
		return err
	}
	err = manifest.VerifyPayload(ownedRoot)
	_ = ownedRoot.Close()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(generation, "manifest.json"), raw, 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	selected := filepath.Join(dir, "generations", hex.EncodeToString(sum[:]))
	if err := os.Rename(generation, selected); err != nil {
		return err
	}
	generation = selected
	policy := GenerationPolicy{Version: 1, GenerationRoot: generation, LogicalInstallRoot: install, ManifestSHA256: hex.EncodeToString(sum[:]), SystemExecutables: artifact.SystemExecutables, LibraryRoots: artifact.LibraryRoots}
	raw, err = json.Marshal(policy)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "launch.json"), raw, 0o644); err != nil {
		return err
	}
	_, _, err = copyRootBinary(bootstrap, filepath.Join(dir, "pw-launch"), 0o4755, nil)
	return err
}

func copyArtifactFile(root *os.Root, row pkgmanifest.PayloadEntry, target string) error {
	source, err := root.Open(row.Path)
	if err != nil {
		return err
	}
	defer source.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(row.Mode))
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, copied := io.Copy(io.MultiWriter(output, hash), source)
	closed := output.Close()
	if copied != nil {
		return copied
	}
	if closed != nil {
		return closed
	}
	if size != *row.Size || hex.EncodeToString(hash.Sum(nil)) != *row.SHA256 {
		return fmt.Errorf("artifact changed during root-owned copy: %s", row.Path)
	}
	return nil
}
