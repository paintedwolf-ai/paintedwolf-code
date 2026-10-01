package scan

import (
	"path/filepath"
	"strings"
)

// EffectiveScanTargets returns a driver's complete filesystem scope.
func EffectiveScanTargets(projectDir string, paths []string) []string {
	projectDir = strings.TrimSpace(projectDir)
	seen := make(map[string]struct{}, len(paths))
	targets := make([]string, 0, len(paths))
	for _, raw := range paths {
		if target := strings.TrimSpace(raw); target != "" {
			clean := filepath.Clean(target)
			if !filepath.IsAbs(clean) && projectDir != "" {
				clean = filepath.Join(projectDir, clean)
			}
			if _, exists := seen[clean]; !exists {
				seen[clean] = struct{}{}
				targets = append(targets, clean)
			}
		}
	}
	if len(targets) == 0 && projectDir != "" {
		return []string{filepath.Clean(projectDir)}
	}
	return targets
}

func BoundRelUnderRoot(absRoot, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	raw = filepath.Clean(raw)
	var abs string
	if filepath.IsAbs(raw) {
		abs = raw
	} else {
		abs = filepath.Join(absRoot, raw)
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	if rel == "." {
		return "", false
	}
	return rel, true
}
