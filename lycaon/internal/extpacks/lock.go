package extpacks

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

const LockFormat = 1

// LockFile is the exact installed package graph.
type LockFile struct {
	LockFormat int             `yaml:"lock_format" json:"lock_format"`
	Packages   []LockedPackage `yaml:"packages" json:"packages"`
}

// LockedPackage records immutable package identity plus the graph edge selected for it.
type LockedPackage struct {
	ID           string            `yaml:"id" json:"id"`
	Version      string            `yaml:"version" json:"version"`
	Source       string            `yaml:"source" json:"source"`
	Ref          string            `yaml:"ref,omitempty" json:"ref,omitempty"`
	Subdir       string            `yaml:"subdir,omitempty" json:"subdir,omitempty"`
	Revision     string            `yaml:"revision,omitempty" json:"revision,omitempty"`
	Integrity    string            `yaml:"integrity" json:"integrity"`
	Kind         PackKind          `yaml:"kind" json:"kind"`
	Dependencies map[string]string `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
}

func EmptyLock() LockFile {
	return LockFile{LockFormat: LockFormat, Packages: []LockedPackage{}}
}

// LoadLockFile strictly reads one exact package graph. A missing file is empty.
func LoadLockFile(path string) (LockFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return EmptyLock(), nil
		}
		return LockFile{}, err
	}
	return ParseLock(path, data)
}

// ValidateLock refuses ambiguity before any package bytes are loaded.
func ValidateLock(lock LockFile) error {
	if lock.LockFormat != LockFormat {
		return &UnsupportedFormatError{Doc: "extensions.lock.yaml", Field: "lock_format", Got: lock.LockFormat, Want: LockFormat}
	}
	seen := map[string]LockedPackage{}
	for _, pkg := range lock.Packages {
		if err := ValidatePackID(pkg.ID); err != nil {
			return fmt.Errorf("package id: %w", err)
		}
		if _, duplicate := seen[pkg.ID]; duplicate {
			return fmt.Errorf("duplicate package %q", pkg.ID)
		}
		if IsStockPackID(pkg.ID) {
			return fmt.Errorf("stock package %s must not appear in an extension lock", pkg.ID)
		}
		seen[pkg.ID] = pkg
		if _, err := parseCanonicalVersion(pkg.Version); err != nil {
			return fmt.Errorf("package %s version: %w", pkg.ID, err)
		}
		if strings.TrimSpace(pkg.Source) == "" {
			return fmt.Errorf("package %s source is required", pkg.ID)
		}
		if pkg.Kind != PackKindGit && pkg.Kind != PackKindPath {
			return fmt.Errorf("package %s kind %q is invalid", pkg.ID, pkg.Kind)
		}
		if pkg.Kind == PackKindGit && strings.TrimSpace(pkg.Revision) == "" {
			return fmt.Errorf("package %s revision is required for git", pkg.ID)
		}
		if pkg.Integrity == "" || !strings.HasPrefix(pkg.Integrity, "sha256:") {
			return fmt.Errorf("package %s integrity must be sha256", pkg.ID)
		}
		for dependencyID, version := range pkg.Dependencies {
			if err := ValidatePackID(dependencyID); err != nil {
				return fmt.Errorf("package %s dependency: %w", pkg.ID, err)
			}
			if _, err := parseCanonicalVersion(version); err != nil {
				return fmt.Errorf("package %s dependency %s: %w", pkg.ID, dependencyID, err)
			}
		}
	}
	for _, pkg := range lock.Packages {
		for dependencyID, dependencyVersion := range pkg.Dependencies {
			if IsStockPackID(dependencyID) {
				stock, ok := stockVersion(dependencyID)
				if !ok || stock != dependencyVersion {
					return fmt.Errorf("package %s dependency %s locks %s, host provides %s", pkg.ID, dependencyID, dependencyVersion, stock)
				}
				continue
			}
			dependency, ok := seen[dependencyID]
			if !ok {
				return fmt.Errorf("package %s dependency %s is absent from lock", pkg.ID, dependencyID)
			}
			if dependency.Version != dependencyVersion {
				return fmt.Errorf("package %s dependency %s locks %s but package entry is %s", pkg.ID, dependencyID, dependencyVersion, dependency.Version)
			}
		}
	}
	return nil
}

// EncodeLock copies, validates, and serializes a lock graph.
func EncodeLock(lock LockFile) ([]byte, error) {
	lock.LockFormat = LockFormat
	lock.Packages = append([]LockedPackage(nil), lock.Packages...)
	normalizeLock(&lock)
	if err := ValidateLock(lock); err != nil {
		return nil, err
	}
	return yaml.Marshal(&lock)
}

func normalizeLock(lock *LockFile) {
	for i := range lock.Packages {
		pkg := &lock.Packages[i]
		pkg.ID = strings.TrimSpace(pkg.ID)
		pkg.Version = strings.TrimSpace(pkg.Version)
		pkg.Source = strings.TrimSpace(pkg.Source)
		pkg.Ref = strings.TrimSpace(pkg.Ref)
		pkg.Revision = strings.TrimSpace(pkg.Revision)
		pkg.Integrity = strings.TrimSpace(pkg.Integrity)
	}
	sort.Slice(lock.Packages, func(i, j int) bool { return lock.Packages[i].ID < lock.Packages[j].ID })
}

func (l LockFile) Package(id string) (LockedPackage, bool) {
	for _, pkg := range l.Packages {
		if pkg.ID == id {
			return pkg, true
		}
	}
	return LockedPackage{}, false
}

// upsertLockedPackage preserves the input backing array.
func upsertLockedPackage(lock LockFile, pkg LockedPackage) LockFile {
	packages := make([]LockedPackage, 0, len(lock.Packages)+1)
	replaced := false
	for _, existing := range lock.Packages {
		if existing.ID == pkg.ID {
			packages = append(packages, pkg)
			replaced = true
			continue
		}
		packages = append(packages, existing)
	}
	if !replaced {
		packages = append(packages, pkg)
	}
	lock.Packages = packages
	return lock
}

// dropLockedPackage preserves the input backing array.
func dropLockedPackage(lock LockFile, id string) LockFile {
	packages := make([]LockedPackage, 0, len(lock.Packages))
	for _, pkg := range lock.Packages {
		if pkg.ID != id {
			packages = append(packages, pkg)
		}
	}
	lock.Packages = packages
	return lock
}

// LoadDeviceLock returns the installed package graph.
func LoadDeviceLock() (LockFile, error) {
	devicePath, err := DeviceLockPath()
	if err != nil {
		return LockFile{}, err
	}
	return LoadLockFile(devicePath)
}

// Pack bounds limit accidental cache growth.
const (
	maxPackBodyBytes = 64 << 20 // 64 MiB
	maxPackBodyFiles = 20_000
)

// PackTreeIntegrity hashes relative names, type and executable mode bits, and
// bytes while excluding VCS/cache metadata.
func PackTreeIntegrity(root string) (string, error) {
	walked, err := walkPackTree(root, "", false)
	return walked.Integrity, err
}

// walkedPackTree captures the bytes covered by integrity.
type walkedPackTree struct {
	Units     []InventoriedUnit
	Manifest  []byte
	Integrity string
}

// walkPackTree hashes and captures units in one pass.
func walkPackTree(root, packID string, captureUnits bool) (walkedPackTree, error) {
	packRoot, err := os.OpenRoot(root)
	if err != nil {
		return walkedPackTree{}, err
	}
	defer func() { _ = packRoot.Close() }()

	h := sha256.New()
	var units []InventoriedUnit
	var manifest []byte
	var totalBytes int64
	var fileCount int
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == PackageBodyMetadataName || rel == MetaPackMetadataName {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if rel != "." {
			// Only owner-exec survives checkout independently of umask.
			exec := '-'
			if !entry.IsDir() && info.Mode()&0o100 != 0 {
				exec = 'x'
			}
			_, _ = fmt.Fprintf(h, "%s\x00%o\x00%c\x00", rel, info.Mode().Type(), exec)
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("pack content %s is not a regular file", rel)
		}
		fileCount++
		totalBytes += info.Size()
		if fileCount > maxPackBodyFiles {
			return fmt.Errorf("pack body holds more than %d files; a pack is configuration, not a build tree", maxPackBodyFiles)
		}
		if totalBytes > maxPackBodyBytes {
			return fmt.Errorf("pack body is over %d MiB; a pack is configuration, not a build tree", maxPackBodyBytes>>20)
		}
		file, err := packRoot.Open(filepath.FromSlash(rel))
		if err != nil {
			return err
		}
		unitID := ""
		if captureUnits {
			unitID = inventoriedUnitIDForRel(packID, rel)
		}
		var copyErr error
		switch {
		case unitID != "":
			var data []byte
			data, copyErr = io.ReadAll(file)
			if copyErr == nil {
				_, _ = h.Write(data)
				units = append(units, InventoriedUnit{
					ID:      unitID,
					Kind:    kindRootForRel(rel),
					Path:    OnDisk(path),
					Content: data,
				})
			}
		case captureUnits && rel == config.PackManifestName:
			manifest, copyErr = io.ReadAll(file)
			if copyErr == nil {
				_, _ = h.Write(manifest)
			}
		default:
			_, copyErr = io.Copy(h, file)
		}
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return walkedPackTree{}, err
	}
	sort.Slice(units, func(i, j int) bool { return units[i].ID < units[j].ID })
	if captureUnits {
		if err := validateUniqueUnitIDs(packID, units); err != nil {
			return walkedPackTree{}, err
		}
	}
	return walkedPackTree{
		Units:     units,
		Manifest:  manifest,
		Integrity: "sha256:" + hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// inventoriedUnitIDForRel excludes hidden payloads and nested dependency trees.
func inventoriedUnitIDForRel(packID, rel string) string {
	parts := strings.Split(rel, "/")
	base := parts[len(parts)-1]
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") {
		return ""
	}
	for _, seg := range parts[:len(parts)-1] {
		if seg == ".git" || seg == "vendor" || seg == "node_modules" {
			return ""
		}
	}
	return UnitIDFor(packID, rel)
}
