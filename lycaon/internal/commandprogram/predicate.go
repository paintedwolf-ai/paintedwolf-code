package commandprogram

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// PredicateFragment preserves quotation after scalar expansion. In particular,
// a quoted fragment inside an otherwise unquoted RHS remains literal.
type PredicateFragment struct {
	Value  string
	Quoted bool
}
type PredicateValue []PredicateFragment

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
// Test words use Expander.ExpandPattern under Scope: no splitting, pathname
// expansion or reparsing; substitution errors stop the containing test.
type PredicateRuntime struct {
	Expansion       Expander
	Scope           ExpansionScope
	Files           PredicateFiles
	Patterns        PredicatePatterns
	Memory          *toolcontract.ProgramMemory
	LocalBoundsOnly bool
	quota           *predicateQuota
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
