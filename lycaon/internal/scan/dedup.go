package scan

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func normalizeCategories(categories []api.ScanCategory) []api.ScanCategory {
	if len(categories) == 0 {
		return nil
	}
	out := append([]api.ScanCategory(nil), categories...)
	sort.Slice(out, func(i, j int) bool {
		return string(out[i]) < string(out[j])
	})
	return out
}

func NormalizeScanPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		path = filepath.ToSlash(filepath.Clean(path))
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func categoriesJSON(categories []api.ScanCategory) (string, error) {
	norm := normalizeCategories(categories)
	b, err := json.Marshal(norm)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
