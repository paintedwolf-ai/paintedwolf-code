package pkgmanifest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// VerifyPayload checks the complete inventory beneath an already retained root.
// The caller owns root identity, owner trust, immutability and lifetime locking.
// excluded names top-level directories the installer keeps beside a package's
// staged payload (its generation store); the payload may not claim them.
func (m *Manifest) VerifyPayload(root *os.Root, excluded ...string) error {
	if root == nil {
		return errors.New("native manifest: missing retained payload root")
	}
	if err := m.Validate(); err != nil {
		return err
	}
	expected := make(map[string]PayloadEntry, len(m.Payload))
	for _, entry := range m.Payload {
		expected[entry.Path] = entry
	}
	for _, name := range excluded {
		if _, claimed := expected[name]; claimed || name == "manifest.json" || strings.Contains(name, "/") || name == "" || name == "." || name == ".." {
			return fmt.Errorf("native manifest: %q cannot be excluded from the payload", name)
		}
	}
	seen := make(map[string]bool, len(expected))
	err := fs.WalkDir(root.FS(), ".", func(name string, actual fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if skip, err := excludedDirectory(name, actual, excluded); skip || err != nil {
			return err
		}
		if name == "manifest.json" {
			if !actual.Type().IsRegular() {
				return errors.New("native manifest: inventory seal is not a physical regular file")
			}
			return nil
		}
		entry, ok := expected[name]
		if !ok {
			return fmt.Errorf("native manifest: unlisted payload object %q", name)
		}
		info, err := actual.Info()
		if err != nil {
			return err
		}
		if uint32(info.Mode().Perm()) != entry.Mode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return fmt.Errorf("native manifest: payload mode differs for %q", name)
		}
		switch entry.Kind {
		case "directory":
			if !info.IsDir() {
				return fmt.Errorf("native manifest: payload directory differs for %q", name)
			}
		case "symlink":
			if info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("native manifest: payload link differs for %q", name)
			}
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			if target != *entry.Target {
				return fmt.Errorf("native manifest: payload link target differs for %q", name)
			}
		case "file":
			if !info.Mode().IsRegular() {
				return fmt.Errorf("native manifest: payload file differs for %q", name)
			}
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			opened, statErr := file.Stat()
			if statErr != nil || !os.SameFile(info, opened) {
				_ = file.Close()
				return fmt.Errorf("native manifest: payload image changed while opening %q", name)
			}
			checked := VerifyOpened(file, Executable{Path: filepath.Join(m.InstallRoot, name), SHA256: *entry.SHA256, Size: *entry.Size})
			closed := file.Close()
			if checked != nil {
				return checked
			}
			if closed != nil {
				return closed
			}
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errors.New("native manifest: payload inventory is incomplete")
	}
	return nil
}

// excludedDirectory skips an installer-owned top-level directory; anything else
// at that name is refused rather than silently ignored.
func excludedDirectory(name string, entry fs.DirEntry, excluded []string) (bool, error) {
	if strings.Contains(name, "/") || !slices.Contains(excluded, name) {
		return false, nil
	}
	if entry.Type()&fs.ModeType != fs.ModeDir {
		return true, fmt.Errorf("native manifest: installer directory %q is not a physical directory", name)
	}
	return true, fs.SkipDir
}
