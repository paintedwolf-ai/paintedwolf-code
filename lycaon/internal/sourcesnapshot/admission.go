package sourcesnapshot

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
)

// fileRef is one admitted regular file as the walk met it. Entry carries the
// listing's stat when the walk had one; a reference built for a single path
// leaves it nil and the capture stats the file itself.
type fileRef struct {
	Path  string
	Abs   string
	Entry fs.DirEntry
}

// stat returns the file's metadata, from the listing when it is held.
func (r fileRef) stat() (os.FileInfo, error) {
	if r.Entry != nil {
		return r.Entry.Info()
	}
	return os.Lstat(r.Abs)
}

// ScopeProvider builds the capture scope for a root.
type ScopeProvider interface {
	Capture(ctx context.Context, root string) *sourcescope.Scope
}

// admittedFiles surveys one root under its scope: the listing minus what
// the floor, the project, an ignore file, or a budget leaves out. Every
// admitted file reaches visit as the walk meets it, so a survey of any size
// holds no listing; the directories the walk did not enter are returned.
func admittedFiles(ctx context.Context, root Root, scope *sourcescope.Scope, visit func(fileRef) error) ([]Boundary, error) {
	return admittedUnder(ctx, root, scope, ".", visit)
}

// admittedUnder surveys the subtree at relDir with the same scope a root
// survey uses, so a delta observation admits exactly what a full one would.
func admittedUnder(ctx context.Context, root Root, scope *sourcescope.Scope, relDir string, visit func(fileRef) error) ([]Boundary, error) {
	relDir = cleanRelDir(relDir)
	var boundaries []Boundary
	walkRoot := root.Path
	var walkScope sandbox.SurveyScope = scope
	if relDir != "." {
		walkRoot = filepath.Join(root.Path, filepath.FromSlash(relDir))
		walkScope = subtreeScope{base: relDir, scope: scope}
	}
	opts := sandbox.SurveyOptions{IncludeHidden: true, Scope: walkScope, Budgets: scope.Budgets(), Stream: !scope.Budgets().Bounded()}
	opts.OnBoundary = func(b sandbox.SurveyBoundary) {
		boundaries = append(boundaries, Boundary{
			RootPath: root.Path, Path: joinRel(relDir, b.Rel),
			Reason: string(b.Reason), Detail: b.Detail, Entries: b.Entries,
		})
	}
	err := sandbox.SurveyWalk(ctx, walkRoot, opts, func(item sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		if item.IsDir || item.IsSymlink {
			return sandbox.SurveyContinue, nil
		}
		rel := joinRel(relDir, filepath.ToSlash(item.Rel))
		if err := visit(fileRef{Path: rel, Abs: absPath(root.Path, rel), Entry: item.DirEntry}); err != nil {
			return sandbox.SurveyStop, err
		}
		return sandbox.SurveyContinue, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].Path < boundaries[j].Path })
	return boundaries, nil
}

// subtreeScope presents a subtree survey to a root-relative scope.
type subtreeScope struct {
	base  string
	scope *sourcescope.Scope
}

func (s subtreeScope) PruneDir(rel, abs string) (string, bool) {
	return s.scope.PruneDir(joinRel(s.base, rel), abs)
}

func (s subtreeScope) AdmitFile(rel, abs string) bool {
	return s.scope.AdmitFile(joinRel(s.base, rel), abs)
}

func joinRel(base, rel string) string {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	switch {
	case base == "." || base == "":
		if rel == "" {
			return "."
		}
		return rel
	case rel == "" || rel == ".":
		return base
	default:
		return base + "/" + rel
	}
}

func cleanRelDir(rel string) string {
	rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	if rel == "" {
		return "."
	}
	return path.Clean(rel)
}

// pathBounds returns the exclusive bounds that select every path strictly
// below dir in byte order: "dir/" and "dir0", since '0' follows '/'.
func pathBounds(dir string) (low, high string) {
	return dir + "/", dir + "0"
}

// underDir reports whether rel lies strictly below dir.
func underDir(rel, dir string) bool {
	if dir == "." {
		return rel != "."
	}
	return strings.HasPrefix(rel, dir+"/")
}

func absPath(rootPath, rel string) string {
	return filepath.Join(rootPath, filepath.FromSlash(rel))
}
