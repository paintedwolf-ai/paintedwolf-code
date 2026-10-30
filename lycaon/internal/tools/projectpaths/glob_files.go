package projectpaths

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/fspath"
)

// GlobFiles is pathname expansion's view of one invocation's logical paths.
// Directories list through the held-root confined walk, and a candidate is a
// match only when an ordinary read operand of it would be admitted. Symbolic
// links are entries, never directories, so a walk never traverses one, except
// along an attached root's own parent chain.
type GlobFiles struct{ Paths *ProgramPaths }

var _ commandsurface.GlobFiles = GlobFiles{}

func (g GlobFiles) ListDir(ctx context.Context, dir string, maximum int) ([]commandsurface.GlobEntry, error) {
	bound, err := g.Paths.Bind(ctx, globOperand(dir), false)
	if err != nil || bound.Null {
		return nil, globRefusal(err)
	}
	file, err := bound.Open()
	if err != nil {
		return nil, globRefusal(err)
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err != nil || !info.IsDir() {
		return nil, globRefusal(err)
	}
	listed, err := file.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, globRefusal(err)
	}
	entries := make([]commandsurface.GlobEntry, 0, len(listed))
	for _, entry := range listed {
		link := entry.Type()&os.ModeSymlink != 0
		entries = append(entries, commandsurface.GlobEntry{Name: entry.Name(), Dir: entry.IsDir() && !link, Link: link})
	}
	return entries, nil
}

// Entry reports a literal component without following a final link. A
// component outside read authority, such as an ancestor of an admitted root,
// is not observed: it continues the walk, and only an admitted directory
// beneath it can list or match.
func (g GlobFiles) Entry(ctx context.Context, path string) (commandsurface.GlobEntry, bool, error) {
	if g.rootParent(path) {
		return commandsurface.GlobEntry{Dir: true}, true, ctx.Err()
	}
	bound, err := g.Paths.Bind(ctx, globOperand(path), false)
	if err != nil {
		if refusal := globRefusal(err); refusal != nil {
			return commandsurface.GlobEntry{}, false, refusal
		}
		return commandsurface.GlobEntry{Dir: true}, true, nil
	}
	if bound.Null {
		return commandsurface.GlobEntry{}, false, nil
	}
	stat, err := bound.Stat(false)
	if err != nil {
		return commandsurface.GlobEntry{}, false, globRefusal(err)
	}
	link := stat.Mode&os.ModeSymlink != 0
	return commandsurface.GlobEntry{Dir: stat.Mode.IsDir() && !link, Link: link}, true, nil
}

// Admit applies the invocation's read authority, with its boundary, to the
// candidate's coordinates beneath its admitted root.
func (g GlobFiles) Admit(ctx context.Context, path string) (bool, error) {
	if g.rootParent(path) {
		return ctx.Err() == nil, ctx.Err()
	}
	bound, err := g.Paths.Bind(ctx, globOperand(path), false)
	if err == nil && bound.Null {
		return false, nil
	}
	if err == nil {
		_, err = ResolveRead(ctx, g.Paths.boundary, g.Paths.context, filepath.Join(bound.Resolved.Root.Path, bound.exactRelative))
	}
	if err != nil {
		return false, globRefusal(err)
	}
	return true, nil
}

// rootParent reports a proper ancestor of an attached root, in either the
// root's own spelling or its canonical one. A walk passes through it to reach
// the root, even across a symbolic link such as a system temporary directory;
// listing it still needs read authority.
func (g GlobFiles) rootParent(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	_, ok := g.Paths.rootParents[filepath.Clean(filepath.FromSlash(path))]
	return ok
}

// rootParentChains resolves each root's parent chain once per invocation.
func rootParentChains(roots []string) map[string]struct{} {
	parents := map[string]struct{}{}
	for _, root := range roots {
		for _, spelling := range []string{filepath.Clean(root), fspath.CanonicalPath(root)} {
			if !filepath.IsAbs(spelling) {
				continue
			}
			for child, dir := spelling, filepath.Dir(spelling); dir != child; child, dir = dir, filepath.Dir(dir) {
				parents[dir] = struct{}{}
			}
		}
	}
	return parents
}

func globOperand(path string) string {
	if path == "" {
		return "."
	}
	return path
}

// globRefusal keeps cancellation and resource limits; any other refusal means
// the walk observes nothing at that path, and the literal word reaches the
// operation's own path authority.
func globRefusal(err error) error {
	var pathErr *PathError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &pathErr) && pathErr.Code == "COMMAND_LIMIT_EXCEEDED":
		return err
	}
	return nil
}
