package command

import (
	"context"
	"errors"
	"io/fs"

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
	return bound.TestPath(operator)
}

// PredicateFiles binds file predicates to this frame's current directory.
func (d *ProgramDispatcher) PredicateFiles() commandprogram.PredicateFiles {
	return framePredicates{paths: d.paths}
}
