package extpacks

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// ErrInvalidPackID rejects non-canonical pack identities.
var ErrInvalidPackID = errors.New("invalid pack id")

const (
	// DeviceDesiredName is the device-scope desired-state filename.
	DeviceDesiredName = "extensions.yaml"
	// DeviceLockName is the device-scope resolved package graph.
	DeviceLockName          = "extensions.lock.yaml"
	PackageBodyMetadataName = ".package-metadata.json"
)

func ProjectDesiredRel() string {
	return settingsoverlay.Rel(DeviceDesiredName)
}

// CacheRoot is {configdir}/extensions.
func CacheRoot() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, enginepaths.ExtensionsCacheDirName), nil
}

// DeviceDesiredPath is {configdir}/extensions.yaml.
func DeviceDesiredPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DeviceDesiredName), nil
}

// DeviceLockPath is {configdir}/extensions.lock.yaml.
func DeviceLockPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DeviceLockName), nil
}

// ProjectDesiredPath is {project}/<overlay>/extensions.yaml.
func ProjectDesiredPath(projectDir string) string {
	return filepath.Join(projectDir, filepath.FromSlash(ProjectDesiredRel()))
}

// packIDSegment is one segment of the pack id grammar the API declares.
var packIDSegment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

// ValidatePackID accepts canonical slash-separated identities.
func ValidatePackID(packID string) error {
	id := packID
	if id == "" {
		return fmt.Errorf("%w: empty", ErrInvalidPackID)
	}
	if strings.TrimSpace(id) != id {
		return fmt.Errorf("%w %q: surrounding whitespace", ErrInvalidPackID, packID)
	}
	if strings.ContainsRune(id, 0) {
		return fmt.Errorf("%w %q: contains NUL", ErrInvalidPackID, packID)
	}
	if strings.Contains(id, `\`) {
		return fmt.Errorf("%w %q: backslash separator", ErrInvalidPackID, packID)
	}
	segs := strings.Split(id, "/")
	for _, seg := range segs {
		switch {
		case seg == "":
			return fmt.Errorf("%w %q: empty segment", ErrInvalidPackID, packID)
		case seg == "." || seg == "..":
			return fmt.Errorf("%w %q: relative segment %q", ErrInvalidPackID, packID, seg)
		case strings.HasPrefix(seg, "."):
			return fmt.Errorf("%w %q: segment %q starts with a dot", ErrInvalidPackID, packID, seg)
		case !packIDSegment.MatchString(seg):
			return fmt.Errorf("%w %q: segment %q uses characters outside letters, digits, '.', '_' and '-'", ErrInvalidPackID, packID, seg)
		}
	}
	return nil
}

// PackIDSafe returns a fixed-width storage key.
func PackIDSafe(packID string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(packID)))
}

func packageCacheDir(packID string) (string, error) {
	if err := ValidatePackID(packID); err != nil {
		return "", err
	}
	root, err := CacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, PackIDSafe(packID)), nil
}

// CachedPackRevisionDir is the immutable body cache for one exact Git revision.
func CachedPackRevisionDir(packID, revision string) (string, error) {
	root, err := packageCacheDir(packID)
	if err != nil {
		return "", err
	}
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return "", fmt.Errorf("cached revision: revision required")
	}
	return filepath.Join(root, "revisions", PackIDSafe(revision)), nil
}
