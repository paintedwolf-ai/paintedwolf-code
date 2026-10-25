package projectpaths

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// GlobFiles is pathname expansion's view of one invocation's logical paths.
// Directories list through the held-root confined walk, and a candidate is a
// match only when an ordinary read operand of it would be admitted. Symbolic
// links are entries, never directories, so a walk never traverses one.
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
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.IsDir() {
		return nil, globRefusal(err)
	}
	listed, err := file.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, globRefusal(err)
	}
	entries := make([]commandsurface.GlobEntry, 0, len(listed))
	for _, entry := range listed {
		childAbs := bound.Resolved.Abs + string(filepath.Separator) + entry.Name()
		if settingsoverlay.HasRecoverySegment(childAbs) || settingsoverlay.IsRecoveryOnlyDir(childAbs) {
			continue
		}
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
	if settingsoverlay.IsRecoveryRelPath(bound.Resolved.ScopeRel) || settingsoverlay.HasRecoverySegment(bound.Resolved.Abs) || settingsoverlay.IsRecoveryOnlyDir(bound.Resolved.Abs) {
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
