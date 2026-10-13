// Package launchverify proves that a running process is one of the installed
// Painted Wolf Code executables, and that its parent is the installed desktop
// app, from the process filesystem and the package's binary manifest.
package launchverify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Executable roles, named by the installed basename the manifest lists.
const (
	RoleEngine     = "pw"
	RoleDesktopApp = "painted-wolf-code"
)

const (
	digestPrefix     = "sha256:"
	maxManifestBytes = 1 << 20
)

// Manifest lists the installed executables by install-relative path and
// content digest. The package writes it beside the executables as root.
type Manifest struct {
	Version     string            `json:"version"`
	Executables map[string]string `json:"executables"`
}

// LoadManifest reads and validates the manifest at path.
func LoadManifest(manifestPath string) (Manifest, error) {
	file, err := os.Open(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, err
	}
	if len(raw) > maxManifestBytes {
		return Manifest{}, fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) validate() error {
	if len(m.Executables) == 0 {
		return errors.New("manifest lists no executables")
	}
	for name, digest := range m.Executables {
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return fmt.Errorf("manifest executable %q is not install-relative", name)
		}
		if _, err := parseDigest(digest); err != nil {
			return fmt.Errorf("manifest executable %q: %w", name, err)
		}
	}
	return nil
}

func parseDigest(value string) ([]byte, error) {
	if !strings.HasPrefix(value, digestPrefix) {
		return nil, errors.New("digest must be sha256")
	}
	sum, err := hex.DecodeString(strings.TrimPrefix(value, digestPrefix))
	if err != nil || len(sum) != sha256.Size {
		return nil, errors.New("digest is not a sha256 hex value")
	}
	return sum, nil
}

// Digest returns the content digest the manifest records for role.
func (m Manifest) Digest(role string) ([]byte, bool) {
	for name, digest := range m.Executables {
		if path.Base(name) != role {
			continue
		}
		sum, err := parseDigest(digest)
		if err != nil {
			return nil, false
		}
		return sum, true
	}
	return nil, false
}

// DigestString formats a content digest the way the manifest records it.
func DigestString(sum []byte) string { return digestPrefix + hex.EncodeToString(sum) }
