// Package sourceworkspace defines physical source root-set identity.
package sourceworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
)

// ID identifies one physical resolution of a project's attached roots.
func ID(projectID string, roots []projectroot.RootRef) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ""
	}
	parts := make([]string, 0, len(roots))
	for _, root := range roots {
		rootID, rootPath := strings.TrimSpace(root.ID), strings.TrimSpace(root.Path)
		if rootID == "" || rootPath == "" {
			continue
		}
		parts = append(parts, rootID+"\x00"+canonicalRoot(rootPath))
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(projectID + "\x00" + strings.Join(parts, "\x00")))
	return "ws_" + hex.EncodeToString(sum[:16])
}

func canonicalRoot(path string) string {
	clean := filepath.Clean(path)
	if absolute, err := filepath.Abs(clean); err == nil {
		clean = absolute
	}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	}
	return clean
}
