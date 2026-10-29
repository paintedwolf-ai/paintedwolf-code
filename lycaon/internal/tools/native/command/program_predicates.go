package command

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// framePredicates answers test, [ and [[ file predicates from the statement's
// directory frame through the program's own path binding: scope and
// confinement refusals stay typed, a missing entry is false.
type framePredicates struct{ paths *projectpaths.ProgramPaths }

var _ commandprogram.PredicateFiles = framePredicates{}

func (p framePredicates) TestPath(ctx context.Context, _ toolcontract.OperationInvocation, operator, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if path == "" {
		return false, nil
	}
	bound, err := p.paths.Bind(ctx, path, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	stat, err := bound.Stat(operator != "-L")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch operator {
	case "-e":
		return true, nil
	case "-f":
		return stat.Mode.IsRegular(), nil
	case "-d":
		return stat.Mode.IsDir(), nil
	case "-L":
		return stat.Mode&fs.ModeSymlink != 0, nil
	case "-r", "-w", "-x":
		return accessible(bound.Resolved.Abs, operator), nil
	}
	return false, errors.New("unsupported file predicate " + operator)
}

// accessible applies the process's own permission check for -r, -w and -x.
func accessible(path, operator string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	mode := info.Mode().Perm()
	switch operator {
	case "-r":
		return mode&0o444 != 0
	case "-w":
		return mode&0o222 != 0
	}
	return mode&0o111 != 0 || info.IsDir()
}

// PredicateFiles binds file predicates to this frame's current directory.
func (d *ProgramDispatcher) PredicateFiles() commandprogram.PredicateFiles {
	return framePredicates{paths: d.paths}
}
