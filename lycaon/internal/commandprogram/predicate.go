package commandprogram

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// PredicateFragment preserves quotation after scalar expansion. In particular,
// a quoted fragment inside an otherwise unquoted RHS remains literal.
type PredicateFragment struct {
	Value  string
	Quoted bool
}
type PredicateValue []PredicateFragment

// PredicateExpansion expands only reached test words without splitting, pathname
// expansion, or executable reparsing. Substitution errors stop the containing test.
type PredicateExpansion interface {
	ExpandPredicate(context.Context, commandsurface.DeferredWord) (PredicateValue, error)
}

// PredicateFiles asks existing filesystem authority about one exact operand.
// A denied probe is an error, never a false predicate or a future write grant.
type PredicateFiles interface {
	TestPath(context.Context, toolcontract.OperationInvocation, string, string) (bool, error)
}

// PredicatePatterns matches glob/ERE data with preserved literal fragments and
// operation-level cancellation/resource admission. It executes no program text.
type PredicatePatterns interface {
	MatchPredicate(context.Context, string, string, PredicateValue) (bool, error)
}

// PredicateRuntime supplies only the ports needed by native boolean operations.
type PredicateRuntime struct {
	Expansion PredicateExpansion
	Files     PredicateFiles
	Patterns  PredicatePatterns
}

// BooleanBuiltin evaluates the scalar true/false/: builtins. Arbitrary operands
// are data; the expansion boundary has already enforced scalar/argv ceilings.
func BooleanBuiltin(ctx context.Context, name string, args []string) (toolcontract.ProducerResult, error) {
	if err := ctx.Err(); err != nil {
		return toolcontract.ProducerResult{}, err
	}
	for _, arg := range args {
		if strings.IndexByte(arg, 0) >= 0 {
			return predicateResult(2), nil
		}
	}
	switch name {
	case "true", ":":
		return predicateResult(0), nil
	case "false":
		return predicateResult(1), nil
	default:
		return predicateResult(2), nil
	}
}

func predicateResult(status int) toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: status < 2, PublicationEligible: status == 0, EffectCertainty: toolcontract.NoEffect}
}
