package evidence

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

var groundingBoundary = sandbox.NewBoundary(sandbox.Config{
	ProjectRootRequired: true,
	RejectSymlinkEscape: true,
}, nil)

// CitationRoots maps citation tokens to ledger display paths.
type CitationRoots struct {
	Roots        []projectroot.RootRef
	ActiveRootID string
	// ProjectDir is used when Roots is empty.
	ProjectDir string
}

// NormalizeLedgerPath is the ByPath key for a path token. Folds \ to /.
func NormalizeLedgerPath(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ReplaceAll(token, `\`, "/"), "./")
}

// LedgerPathToken is the ByPath key when token is path-shaped. Empty for kind#N.
func LedgerPathToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" || strings.Contains(token, "#") {
		return ""
	}
	if !strings.ContainsAny(token, `/\.`) {
		return ""
	}
	return NormalizeLedgerPath(token)
}

// NormalizeCitationPath maps a citation token to a repo-relative path using sandbox jail rules.
func NormalizeCitationPath(projectDir, token string) (string, bool) {
	_, rel, ok := ResolveCitationAbs(projectDir, token)
	return rel, ok
}

// ResolveCitationAbs resolves a citation inside the project boundary.
func ResolveCitationAbs(projectDir, token string) (absPath, relPath string, ok bool) {
	token = NormalizeLedgerPath(token)
	if token == "" || sandbox.HasParentTraversal(token) {
		return "", "", false
	}

	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return "", "", false
	}

	abs, err := groundingBoundary.ResolveAbs(projectDir, token)
	if err != nil {
		return "", "", false
	}
	root, err := groundingBoundary.ResolveAbs(projectDir, ".")
	if err != nil {
		return "", "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", false
	}
	return abs, rel, true
}

// LedgerPathKey prefers an observed key for a citation token.
func LedgerPathKey(roots CitationRoots, token string, byPath map[string][]string) string {
	token = NormalizeLedgerPath(token)
	if token == "" || sandbox.HasParentTraversal(token) {
		return ""
	}
	if byPath != nil {
		if len(byPath[token]) > 0 {
			return token
		}
	}
	if key, ok := qualifyCitationToken(roots, token); ok {
		if byPath == nil || len(byPath[key]) > 0 {
			return key
		}
		// Bare paths may belong to any configured root.
		if !strings.HasPrefix(token, "@") && len(roots.Roots) > 1 {
			for _, r := range roots.Roots {
				if key2, ok2 := qualifyCitationToken(CitationRoots{
					Roots: roots.Roots, ActiveRootID: r.ID, ProjectDir: roots.ProjectDir,
				}, token); ok2 && len(byPath[key2]) > 0 {
					return key2
				}
			}
		}
		return key
	}
	if byPath != nil {
		return ""
	}
	return token
}

func qualifyCitationToken(roots CitationRoots, token string) (string, bool) {
	if len(roots.Roots) > 0 {
		abs, root, err := projectroot.ResolveAbs(roots.Roots, roots.ActiveRootID, token)
		if err != nil {
			return "", false
		}
		primary, err := projectroot.PrimaryRoot(roots.Roots)
		if err != nil {
			return "", false
		}
		return projectroot.Qualify(primary, root, abs), true
	}
	if rel, ok := NormalizeCitationPath(roots.ProjectDir, token); ok {
		return rel, true
	}
	return "", false
}

// ResolveCitationFile finds an openable file across configured roots.
func ResolveCitationFile(roots CitationRoots, token string) (absPath, displayPath string, ok bool) {
	token = NormalizeLedgerPath(token)
	if token == "" || sandbox.HasParentTraversal(token) {
		return "", "", false
	}
	try := func(activeID string) (string, string, bool) {
		ctx := CitationRoots{Roots: roots.Roots, ActiveRootID: activeID, ProjectDir: roots.ProjectDir}
		var abs string
		var display string
		if len(ctx.Roots) > 0 {
			resolved, root, err := projectroot.ResolveAbs(ctx.Roots, ctx.ActiveRootID, token)
			if err != nil {
				return "", "", false
			}
			abs = resolved
			primary, err := projectroot.PrimaryRoot(ctx.Roots)
			if err != nil {
				return "", "", false
			}
			display = projectroot.Qualify(primary, root, abs)
		} else {
			var rel string
			var ok bool
			abs, rel, ok = ResolveCitationAbs(ctx.ProjectDir, token)
			if !ok {
				return "", "", false
			}
			display = rel
		}
		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return "", "", false
		}
		return abs, display, true
	}
	if abs, display, ok := try(roots.ActiveRootID); ok {
		return abs, display, true
	}
	if strings.HasPrefix(token, "@") || len(roots.Roots) <= 1 {
		return "", "", false
	}
	for _, r := range roots.Roots {
		if abs, display, ok := try(r.ID); ok {
			return abs, display, true
		}
	}
	return "", "", false
}

// IsOpenablePath reports file resolution and returns nil when no check is possible.
func IsOpenablePath(roots CitationRoots, pathQuery string) *bool {
	pathQuery = strings.TrimSpace(pathQuery)
	if pathQuery == "" {
		return nil
	}
	if len(roots.Roots) > 0 {
		if _, _, ok := ResolveCitationFile(roots, pathQuery); ok {
			b := true
			return &b
		}
		b := false
		return &b
	}
	projectDir := strings.TrimSpace(roots.ProjectDir)
	if projectDir == "" {
		return nil
	}
	abs, _, ok := ResolveCitationAbs(projectDir, pathQuery)
	if !ok {
		b := false
		return &b
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		b := false
		return &b
	}
	b := true
	return &b
}

// SplitPathLineToken splits a positive line suffix from a path token.
func SplitPathLineToken(raw string) (path string, line int, ok bool) {
	i := strings.LastIndex(raw, ":")
	if i <= 0 {
		return "", 0, false
	}
	lineStr := raw[i+1:]
	if lineStr == "" {
		return "", 0, false
	}
	for _, c := range lineStr {
		if c < '0' || c > '9' {
			return "", 0, false
		}
	}
	n, err := strconv.Atoi(lineStr)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return raw[:i], n, true
}
