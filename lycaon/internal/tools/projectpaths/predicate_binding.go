package projectpaths

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// PredicateFiles binds native predicates to an already admitted operation's read
// session. The host constructs that session from the invocation's actual scope;
// the predicate cannot substitute a scope or promote -w into write authority.
type PredicateFiles struct {
	Reads      *ReadSession
	Invocation toolcontract.OperationInvocation
}

func (p PredicateFiles) TestPath(ctx context.Context, inv toolcontract.OperationInvocation, operator, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if p.Reads == nil || inv != p.Invocation {
		return false, fmt.Errorf("predicate read session does not match invocation")
	}
	if err := inv.Validate(); err != nil {
		return false, err
	}
	resolved, err := p.Reads.Resolve(ctx, path)
	if err != nil {
		return false, err
	}
	result, err := p.Reads.TestPath(resolved, operator, path)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return result, nil
}
