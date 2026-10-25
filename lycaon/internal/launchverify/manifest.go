// Package launchverify proves that a running process is one of the installed
// Painted Wolf Code executables, and that its parent is the installed desktop
// app, from the process filesystem and the package's executable manifest.
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

// Installed paths the package manifest lists for each role.
const (
	EnginePath     = "/opt/painted-wolf-code/bin/pw"
	DesktopAppPath = "/opt/painted-wolf-code/bin/painted-wolf-code"
)

func rolePath(role, root string) string {
	if root == "" {
		root = "/opt/painted-wolf-code"
	}
	switch role {
	case RoleEngine:
		return filepath.Join(root, "bin", "pw")
	case RoleDesktopApp:
		return filepath.Join(root, "bin", "painted-wolf-code")
	default:
		return ""
	}
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
