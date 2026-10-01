package projectstack

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/sandbox"
)

const stackManifestMaxDepth = 2

// manifestBasenames are dependency manifest files discovered by shallow walk.
var manifestBasenames = map[string]struct{}{
	"go.mod":           {},
	"package.json":     {},
	"Cargo.toml":       {},
	"pyproject.toml":   {},
	"requirements.txt": {},
	"Gemfile":          {},
	"composer.json":    {},
	"cpanfile":         {},
	"META.json":        {},
	"META.yml":         {},
}

// discoverManifests returns repo-relative paths to manifest files under root
// up to stackManifestMaxDepth, sorted for stable fingerprinting.
func discoverManifests(ctx context.Context, root string) ([]string, error) {
	var paths []string
	err := sandbox.SurveyWalk(ctx, root, sandbox.SurveyOptions{MaxDepth: stackManifestMaxDepth}, func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		if e.IsDir {
			return sandbox.SurveyContinue, nil
		}
		if _, ok := manifestBasenames[filepath.Base(e.Rel)]; !ok {
			return sandbox.SurveyContinue, nil
		}
		paths = append(paths, filepath.ToSlash(e.Rel))
		return sandbox.SurveyContinue, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func manifestAbs(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}
