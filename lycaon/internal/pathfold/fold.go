// Package pathfold bounds a set of changed slash-relative paths by folding
// the deepest of them into their parent directories.
package pathfold

import "strings"

// Fold replaces the deepest paths with their parent directories until at
// most limit remain, merging the values that land on one directory. It
// reports false when fitting would need the root itself.
func Fold[V any](paths map[string]V, limit int, merge func(V, V) V) (map[string]V, bool) {
	for len(paths) > limit {
		deepest := 0
		for rel := range paths {
			deepest = max(deepest, strings.Count(rel, "/"))
		}
		if deepest == 0 {
			return paths, false
		}
		folded := make(map[string]V, len(paths))
		for rel, value := range paths {
			if strings.Count(rel, "/") == deepest {
				rel = rel[:strings.LastIndex(rel, "/")]
			}
			if prior, ok := folded[rel]; ok {
				value = merge(prior, value)
			}
			folded[rel] = value
		}
		paths = folded
	}
	return paths, true
}
