package survey

import (
	"path"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
)

// ignoreScope prunes project-ignored entries from a discovery walk whose root
// may sit below the project root. A walk root that is itself ignored keeps
// everything: naming the path is the request to look inside it.
type ignoreScope struct {
	base  string
	scope *sourcescope.Scope
}

// newIgnoreScope returns nil when ignore rules do not apply to this walk.
func newIgnoreScope(root projectroot.RootRef, fullRoot string) sandbox.SurveyScope {
	base := projectroot.ScopeRel(root, fullRoot)
	scope := sourcescope.New(root.Path, sourcescope.Options{Plane: sourcescope.Plane{IgnoreFiles: true}})
	if base != "." && !scope.AdmitPath(base, true) {
		return nil
	}
	return ignoreScope{base: base, scope: scope}
}

func (s ignoreScope) rel(rel string) string {
	if s.base == "" || s.base == "." {
		return rel
	}
	return path.Join(s.base, rel)
}

func (s ignoreScope) PruneDir(rel, abs string) (string, bool) {
	return s.scope.PruneDir(s.rel(rel), abs)
}

func (s ignoreScope) AdmitFile(rel, abs string) bool {
	return s.scope.AdmitFile(s.rel(rel), abs)
}

// admitsCatalogEntry applies the scope to a walk-relative catalog entry.
func admitsCatalogEntry(scope sandbox.SurveyScope, rel string, isDir bool) bool {
	if scope == nil {
		return true
	}
	if isDir {
		_, prune := scope.PruneDir(rel, "")
		return !prune
	}
	return scope.AdmitFile(rel, "")
}
