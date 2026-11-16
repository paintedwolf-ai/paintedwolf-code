package survey

import (
	"context"

	"github.com/lycaon/lycaon/internal/sourcescope"
)

type boundaryToolScopes struct{}

func (boundaryToolScopes) Catalog(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: sourcescope.Plane{BoundaryDirectories: []string{"node_modules"}, NestedCheckouts: true}})
}
