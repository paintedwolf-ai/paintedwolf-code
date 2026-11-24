package protectedpath

import (
	"path/filepath"
	"strings"
	"sync/atomic"

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

var (
	staticRuleSetCompilations   atomic.Int64
	candidateSegmentations      atomic.Int64
	compiledCredentialFiles     = compileStaticCredentialRules(credentialFileGlobs)
	compiledCredentialTemplates = compileStaticCredentialRules(credentialTemplateGlobs)
)

func compileStaticCredentialRules(patterns []string) pathglob.RuleSet {
	staticRuleSetCompilations.Add(1)
	return pathglob.Compile(patterns)
}

// IsCredentialFile reports whether a path holds credentials.
func IsCredentialFile(path string) bool {
	cand, ok := prepareCredentialCandidate(path)
	if !ok {
		return false
	}
	return compiledCredentialFiles.MatchCandidate(cand) && !compiledCredentialTemplates.MatchCandidate(cand)
}

// IsCredentialTemplate reports whether a path is a credential example.
func IsCredentialTemplate(path string) bool {
	cand, ok := prepareCredentialCandidate(path)
	return ok && compiledCredentialTemplates.MatchCandidate(cand)
}

func prepareCredentialCandidate(path string) (pathglob.Candidate, bool) {
	rel, ok := normalizeCredentialRel(path)
	if !ok {
		return pathglob.Candidate{}, false
	}
	candidateSegmentations.Add(1)
	return pathglob.PrepareCandidate(rel), true
}

func normalizeCredentialRel(path string) (string, bool) {
	rel := filepath.ToSlash(strings.TrimSpace(path))
	if strings.HasPrefix(rel, "@") {
		// A root-qualified path ("@web/.env") names the same repo-relative file.
		idx := strings.Index(rel, "/")
		if idx <= 0 {
			return "", false
		}
		rel = rel[idx+1:]
	}
	rel = strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+rel)), "/")
	if rel == "" || rel == "." {
		return "", false
	}
	return rel, true
}

// MatchPathGlobs matches a repository-relative, root-qualified, or absolute
// path against slash globs.
func MatchPathGlobs(patterns []string, path string) bool {
	rel, ok := normalizeCredentialRel(path)
	if !ok {
		return false
	}
	for _, pattern := range patterns {
		if pathglob.Match(pattern, rel) {
			return true
		}
	}
	return false
}
