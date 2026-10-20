package commandprogram

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"strings"
)

// TestExpression evaluates the detached [[ tree without serializing/reparsing
// syntax. It validates all node shapes first; unreached words stay deferred.
func (r PredicateRuntime) TestExpression(ctx context.Context, inv toolcontract.OperationInvocation, e commandsurface.TestExpression) (toolcontract.ProducerResult, error) {
	var err error
	r, err = r.withQuota(ctx)
	if err != nil {
		return toolcontract.ProducerResult{}, err
	}
	defer r.quota.close()
	var count int64
	b := predicateBounds()
	if err := validatePredicate(e.Expression, 1, &count, b); err != nil {
		return predicateFailure(err)
	}
	value, err := r.evalTyped(ctx, inv, e.Expression)
	return predicateOutcome(value, err)
}
func validatePredicate(e commandsurface.TestNode, depth int64, count *int64, b predicateLimits) error {
	*count++
	if *count > b.nodes {
		return &PredicateLimitError{"ast_nodes", b.nodes, *count}
	}
	if depth > b.depth {
		return &PredicateLimitError{"ast_depth", b.depth, depth}
	}
	switch n := e.(type) {
	case commandsurface.TestWord:
		return nil
	case commandsurface.GroupedPredicate:
		return validatePredicate(n.Expression, depth+1, count, b)
	case commandsurface.UnaryPredicate:
		if n.Operator != "!" && !predicateUnary(n.Operator) {
			return errPredicateUsage
		}
		if n.Operator != "!" {
			if _, ok := n.Operand.(commandsurface.TestWord); !ok {
				return errPredicateUsage
			}
		}
		return validatePredicate(n.Operand, depth+1, count, b)
	case commandsurface.BinaryPredicate:
		if n.Operator != "&&" && n.Operator != "||" {
			if !predicateBinary(n.Operator) && n.Operator != "==" && n.Operator != "=~" {
				return errPredicateUsage
			}
			if _, ok := n.Left.(commandsurface.TestWord); !ok {
				return errPredicateUsage
			}
			if _, ok := n.Right.(commandsurface.TestWord); !ok {
				return errPredicateUsage
			}
		}
		if err := validatePredicate(n.Left, depth+1, count, b); err != nil {
			return err
		}
		return validatePredicate(n.Right, depth+1, count, b)
	}
	return errPredicateUsage
}
func (r PredicateRuntime) expand(ctx context.Context, n commandsurface.TestNode) (PredicateValue, error) {
	word, ok := n.(commandsurface.TestWord)
	if !ok || r.Expansion == nil {
		return nil, errors.New("predicate expansion port unavailable")
	}
	value, err := r.Expansion.ExpandPredicate(ctx, word.Word)
	if err != nil {
		return nil, err
	}
	b := predicateBounds()
	var size int64
	for _, f := range value {
		size += int64(len(f.Value))
		if strings.IndexByte(f.Value, 0) >= 0 {
			return nil, errPredicateUsage
		}
		if size > b.argument {
			return nil, &PredicateLimitError{"argument_bytes", b.argument, size}
		}
	}
	return value, nil
}
func predicateText(value PredicateValue) string {
	var out strings.Builder
	for _, f := range value {
		out.WriteString(f.Value)
	}
	return out.String()
}
func (r PredicateRuntime) evalTyped(ctx context.Context, inv toolcontract.OperationInvocation, e commandsurface.TestNode) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	switch n := e.(type) {
	case commandsurface.TestWord:
		v, err := r.expand(ctx, n)
		if err != nil {
			return false, err
		}
		text, err := r.text(v)
		return text != "", err
	case commandsurface.GroupedPredicate:
		return r.evalTyped(ctx, inv, n.Expression)
	case commandsurface.UnaryPredicate:
		if n.Operator == "!" {
			v, err := r.evalTyped(ctx, inv, n.Operand)
			return !v, err
		}
		v, err := r.expand(ctx, n.Operand)
		if err != nil {
			return false, err
		}
		text, err := r.text(v)
		if err != nil {
			return false, err
		}
		return r.unary(ctx, inv, n.Operator, text)
	case commandsurface.BinaryPredicate:
		if n.Operator == "&&" || n.Operator == "||" {
			v, err := r.evalTyped(ctx, inv, n.Left)
			if err != nil {
				return false, err
			}
			if n.Operator == "&&" && !v || n.Operator == "||" && v {
				return v, nil
			}
			return r.evalTyped(ctx, inv, n.Right)
		}
		lhs, err := r.expand(ctx, n.Left)
		if err != nil {
			return false, err
		}
		rhs, err := r.expand(ctx, n.Right)
		if err != nil {
			return false, err
		}
		text, err := r.text(lhs)
		if err != nil {
			return false, err
		}
		return r.compare(ctx, n.Operator, text, rhs, true)
	}
	return false, errPredicateUsage
}
