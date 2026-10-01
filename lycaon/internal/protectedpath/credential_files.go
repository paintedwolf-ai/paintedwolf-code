package protectedpath

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/pkg/pathglob"
)

// credentialFileGlobs classify credential files. A write to one always reaches
// the approval gate as a protected subject; overlays cannot remove the class.
var credentialFileGlobs = []string{
	"**/.env",
	"**/.env.*",
	"**/*.pem",
	"**/id_rsa*",
	"**/id_ed25519*",
	"**/id_ecdsa*",
	"**/.ssh/*",
	"**/.aws/**",
	"**/.kube/**",
	"**/.config/gcloud/**",
	"**/.azure/**",
	"**/.docker/**",
	"**/.npmrc",
	"**/.pypirc",
	"**/.netrc",
}

// credentialTemplateGlobs identify value-free credential examples.
var credentialTemplateGlobs = []string{
	"**/*.example",
	"**/*.sample",
	"**/*.template",
	"**/*.dist",
	"**/*.example.*",
	"**/*.sample.*",
}

// IsCredentialFile reports whether a path holds credentials.
func IsCredentialFile(path string) bool {
	return MatchPathGlobs(credentialFileGlobs, path) && !IsCredentialTemplate(path)
}

// IsCredentialTemplate reports whether a path is a credential example.
func IsCredentialTemplate(path string) bool {
	return MatchPathGlobs(credentialTemplateGlobs, path)
}

// MatchPathGlobs matches a repository-relative, root-qualified, or absolute
// path against slash globs.
func MatchPathGlobs(patterns []string, path string) bool {
	rel := filepath.ToSlash(strings.TrimSpace(path))
	if strings.HasPrefix(rel, "@") {
		// A root-qualified path ("@web/.env") names the same repo-relative file.
		idx := strings.Index(rel, "/")
		if idx <= 0 {
			return false
		}
		rel = rel[idx+1:]
	}
	rel = strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+rel)), "/")
	if rel == "" || rel == "." {
		return false
	}
	for _, pattern := range patterns {
		if pathglob.Match(pattern, rel) {
			return true
		}
	}
	return false
}
