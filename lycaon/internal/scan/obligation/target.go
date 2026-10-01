package obligation

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PathScopedTarget returns exact normalized paths. Source-operation facts carry
// deletions separately, so target selection never probes mutable host paths.
func PathScopedTarget(projectDir string, changeSet []string) []string {
	root, err := filepath.Abs(strings.TrimSpace(projectDir))
	if err != nil || strings.TrimSpace(projectDir) == "" || len(changeSet) == 0 {
		return nil
	}
	targets := make([]string, 0, len(changeSet))
	seen := make(map[string]struct{}, len(changeSet))
	for _, raw := range changeSet {
		rel, ok := relativeUnder(root, raw)
		if !ok {
			continue
		}
		if _, duplicate := seen[rel]; duplicate {
			continue
		}
		seen[rel] = struct{}{}
		targets = append(targets, rel)
	}
	sort.Strings(targets)
	return targets
}

func relativeUnder(root, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	clean := filepath.Clean(raw)
	abs := clean
	if !filepath.IsAbs(clean) {
		abs = filepath.Join(root, clean)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
