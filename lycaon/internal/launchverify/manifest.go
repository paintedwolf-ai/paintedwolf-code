// Package launchverify proves that a running process is one of the installed
// Painted Wolf Code executables, and that its parent is the installed desktop
// app, from the process filesystem and the peer's running package generation.
package launchverify

import (
	"encoding/hex"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

// Executable roles, named for refusals.
const (
	RoleEngine     = "pw"
	RoleDesktopApp = "painted-wolf-code"
)

// Installed paths the package manifest lists for each role under the default root.
const (
	EnginePath     = "/opt/painted-wolf-code/bin/pw"
	DesktopAppPath = "/opt/painted-wolf-code/bin/painted-wolf-code"
)

func roleRelativePath(role string) string {
	switch role {
	case RoleEngine:
		return "bin/pw"
	case RoleDesktopApp:
		return "bin/painted-wolf-code"
	default:
		return ""
	}
}

func rolePath(role, root string) string {
	relative := roleRelativePath(role)
	if relative == "" {
		return ""
	}
	if root == "" {
		root = DefaultInstallRoot
	}
	return filepath.Join(root, relative)
}

// expected returns the digest and size the manifest records for role.
func expected(manifest *pkgmanifest.Manifest, role, root string) ([]byte, int64, bool) {
	if manifest == nil {
		return nil, 0, false
	}
	if root != "" && (!cleanAbsolute(root) || manifest.InstallRoot != root) {
		return nil, 0, false
	}
	entry, ok := manifest.Lookup(rolePath(role, root))
	if !ok {
		return nil, 0, false
	}
	sum, err := hex.DecodeString(entry.SHA256)
	if err != nil {
		return nil, 0, false
	}
	return sum, entry.Size, true
}
