package hostenv

import (
	"context"

	"github.com/lycaon/lycaon/internal/hostos"
)

// HostFacts answers identity and system queries. hostos.Host reads the actual
// platform and returns hostos.ErrUnsupported for a fact the platform does not
// provide; callers that need deterministic answers inject their own facts.
type HostFacts interface {
	System(context.Context) (hostos.System, error)
	Identity(context.Context) (hostos.Identity, error)
	// Processors is the number of processing units available to the host
	// process (nproc), and with all every unit installed (nproc --all).
	Processors(ctx context.Context, all bool) (int, error)
}

var _ HostFacts = hostos.Host{}
