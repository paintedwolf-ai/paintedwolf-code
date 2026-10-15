// Package pkgmanifest reads the root-owned manifest a Linux package installs
// beside the host, so the engine can verify the executables it launches against
// the digests the package recorded.
package pkgmanifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/dotversion"
	"github.com/lycaon/lycaon/internal/platformfloor"
)

// FormatVersion is the only manifest shape this reader accepts.
const FormatVersion = 1

// DefaultPath is where the package installs the manifest.
const DefaultPath = "/opt/painted-wolf-code/manifest.json"

// maxManifestBytes bounds a read of a root-owned file that is normally a few KiB.
const maxManifestBytes = 4 << 20

// Manifest records what the package installed.
type Manifest struct {
	FormatVersion int          `json:"format_version"`
	Package       string       `json:"package"`
	Version       string       `json:"version"`
	Architecture  string       `json:"architecture"`
	InstallRoot   string       `json:"install_root"`
	Executables   []Executable `json:"executables"`
}

// Executable is one installed file with its recorded digest.
type Executable struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// UnsupportedFormatError reports a manifest written for another reader.
type UnsupportedFormatError struct{ Found int }

func (e *UnsupportedFormatError) Error() string {
	return fmt.Sprintf("native manifest: format_version %d, want %d", e.Found, FormatVersion)
}

// MismatchError reports a file whose content differs from the manifest entry.
type MismatchError struct{ Path, Want, Got string }

func (e *MismatchError) Error() string {
	return fmt.Sprintf("native manifest: %s has sha256 %s, want %s", e.Path, e.Got, e.Want)
}

// UnlistedError reports a path the manifest does not describe.
type UnlistedError struct{ Path string }

func (e *UnlistedError) Error() string {
	return fmt.Sprintf("native manifest: %s is not listed", e.Path)
}

// Load reads and validates the manifest at path.
func Load(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("native manifest: read %s: %w", path, err)
	}
	if len(raw) > maxManifestBytes {
		return nil, fmt.Errorf("native manifest: %s exceeds %d bytes", path, maxManifestBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("native manifest: decode %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("native manifest: %s carries data after the document", path)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks every required field against the format contract.
func (m *Manifest) Validate() error {
	if m.FormatVersion != FormatVersion {
		return &UnsupportedFormatError{Found: m.FormatVersion}
	}
	if m.Package == "" {
		return errors.New("native manifest: package is empty")
	}
	if err := validateVersion(m.Version); err != nil {
		return err
	}
	if !slices.Contains(platformfloor.LinuxELFArches(), m.Architecture) {
		return fmt.Errorf("native manifest: architecture %q is not shipped", m.Architecture)
	}
	if !isCleanAbsolute(m.InstallRoot) {
		return fmt.Errorf("native manifest: install_root %q must be absolute and clean", m.InstallRoot)
	}
	if len(m.Executables) == 0 {
		return errors.New("native manifest: executables is empty")
	}
	seen := make(map[string]struct{}, len(m.Executables))
	for _, exe := range m.Executables {
		if !isCleanAbsolute(exe.Path) {
			return fmt.Errorf("native manifest: executable path %q must be absolute and clean", exe.Path)
		}
		if _, dup := seen[exe.Path]; dup {
			return fmt.Errorf("native manifest: executable %q is listed twice", exe.Path)
		}
		seen[exe.Path] = struct{}{}
		if !isLowerHexDigest(exe.SHA256) {
			return fmt.Errorf("native manifest: executable %q sha256 must be 64 lowercase hex characters", exe.Path)
		}
		if exe.Size < 0 {
			return fmt.Errorf("native manifest: executable %q has negative size", exe.Path)
		}
	}
	return nil
}

// validateVersion accepts a dotted numeric core with an optional pre-release suffix.
func validateVersion(v string) error {
	core, _, _ := strings.Cut(v, "-")
	if _, err := dotversion.Compare(core, core); err != nil {
		return fmt.Errorf("native manifest: version %q: %w", v, err)
	}
	return nil
}

func isCleanAbsolute(p string) bool {
	return p != "" && filepath.IsAbs(p) && filepath.Clean(p) == p
}

func isLowerHexDigest(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Lookup finds the manifest entry for path.
func (m *Manifest) Lookup(path string) (Executable, bool) {
	for _, exe := range m.Executables {
		if exe.Path == path {
			return exe, true
		}
	}
	return Executable{}, false
}

// Verify hashes the file at path and compares it with its manifest entry.
func (m *Manifest) Verify(path string) error {
	want, ok := m.Lookup(path)
	if !ok {
		return &UnlistedError{Path: path}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if n != want.Size || got != want.SHA256 {
		return &MismatchError{Path: path, Want: want.SHA256, Got: got}
	}
	return nil
}
