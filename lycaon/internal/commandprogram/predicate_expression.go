package commandprogram

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

var errPredicateUsage = errors.New("malformed native predicate")

// PredicateLimitError is a stopping resource refusal, not an ordinary false test.
type PredicateLimitError struct {
	Resource          string
	Maximum, Observed int64
}

func (e *PredicateLimitError) Error() string { return "predicate resource limit: " + e.Resource }

type predicateLimits struct{ nodes, depth, argument, argv int64 }

var predicateBounds = sync.OnceValue(func() predicateLimits {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		panic(err)
	}
	var b predicateLimits
	for _, l := range catalog.Language.Limits {
		switch l.ID {
		case "ast_nodes":
			b.nodes = l.Maximum
		case "ast_depth":
			b.depth = l.Maximum
		case "argument_bytes":
			b.argument = l.Maximum
		case "argv_bytes":
			b.argv = l.Maximum
		}
	}
	return b
})

type predicateExpr struct {
	op, value   string
	left, right *predicateExpr
	depth       int64
}
type predicateParser struct {
	args   []string
	pos    int
	nodes  int64
	bounds predicateLimits
}

func (p *predicateParser) node(op, value string, left, right *predicateExpr) (*predicateExpr, error) {
	p.nodes++
	if p.nodes > p.bounds.nodes {
		return nil, &PredicateLimitError{"ast_nodes", p.bounds.nodes, p.nodes}
	}
	depth := int64(1)
	for _, child := range []*predicateExpr{left, right} {
		if child != nil && child.depth >= depth {
			depth = child.depth + 1
		}
	}
	if depth > p.bounds.depth {
		return nil, &PredicateLimitError{"ast_depth", p.bounds.depth, depth}
	}
	return &predicateExpr{op: op, value: value, left: left, right: right, depth: depth}, nil
}
func (p *predicateParser) expression(level int, depth int64) (*predicateExpr, error) {
	if depth > p.bounds.depth {
		return nil, &PredicateLimitError{"ast_depth", p.bounds.depth, depth}
	}
	if level < 2 {
		lhs, err := p.expression(level+1, depth)
		if err != nil {
			return nil, err
		}
		op := "-o"
		if level == 1 {
			op = "-a"
		}
		for p.pos < len(p.args) && p.args[p.pos] == op {
			p.pos++
			rhs, err := p.expression(level+1, depth+1)
			if err != nil {
				return nil, err
			}
			lhs, err = p.node(op, "", lhs, rhs)
			if err != nil {
				return nil, err
			}
		}
		return lhs, nil
	}
	if p.pos >= len(p.args) {
		return nil, errPredicateUsage
	}
	remaining := len(p.args) - p.pos
	// Binary comparison operands remain data even when they resemble unary flags.
	if remaining >= 3 && predicateBinary(p.args[p.pos+1]) {
		lhs, err := p.node("word", p.args[p.pos], nil, nil)
		if err != nil {
			return nil, err
		}
		rhs, err := p.node("word", p.args[p.pos+2], nil, nil)
		if err != nil {
			return nil, err
		}
		op := p.args[p.pos+1]
		if op != "=" && op != "!=" {
			if _, ok := decimalOperand(lhs.value); !ok {
				return nil, errPredicateUsage
			}
			if _, ok := decimalOperand(rhs.value); !ok {
				return nil, errPredicateUsage
			}
		}
		p.pos += 3
		return p.node(op, "", lhs, rhs)
	}
	word := p.args[p.pos]
	p.pos++
	if remaining == 1 {
		return p.node("word", word, nil, nil)
	}
	switch word {
	case "!":
		child, err := p.expression(2, depth+1)
		if err != nil {
			return nil, err
		}
		return p.node("!", "", child, nil)
	case "(":
		child, err := p.expression(0, depth+1)
		if err != nil {
			return nil, err
		}
		if p.pos >= len(p.args) || p.args[p.pos] != ")" {
			return nil, errPredicateUsage
		}
		p.pos++
		return child, nil
	}
	if predicateUnary(word) {
		if p.pos >= len(p.args) {
			return nil, errPredicateUsage
		}
		operand, err := p.node("word", p.args[p.pos], nil, nil)
		if err != nil {
			return nil, err
		}
		p.pos++
		return p.node(word, "", operand, nil)
	}
	if word == ")" || word == "-a" || word == "-o" {
		return nil, errPredicateUsage
	}
	return p.node("word", word, nil, nil)
}
func predicateUnary(op string) bool {
	switch op {
	case "-n", "-z", "-e", "-f", "-d", "-L", "-r", "-w", "-x":
		return true
	}
	return false
}
func predicateBinary(op string) bool {
	switch op {
	case "=", "!=", "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
		return true
	}
	return false
}

// TestBuiltin validates the entire argv expression before any authority-backed
// probe. Operands already underwent ordinary command expansion at dispatch.
func (r PredicateRuntime) TestBuiltin(ctx context.Context, invocation toolcontract.OperationInvocation, name string, args []string) (toolcontract.ProducerResult, error) {
	if err := ctx.Err(); err != nil {
		return toolcontract.ProducerResult{}, err
	}
	if name == "[" {
		if len(args) == 0 || args[len(args)-1] != "]" {
			return predicateResult(2), nil
		}
		args = args[:len(args)-1]
	} else if name != "test" {
		return predicateResult(2), nil
	}
	b := predicateBounds()
	var total int64
	for _, arg := range args {
		if strings.IndexByte(arg, 0) >= 0 {
			return predicateResult(2), nil
		}
		n := int64(len(arg))
		total += n + 1
		if n > b.argument {
			return toolcontract.ProducerResult{}, &PredicateLimitError{"argument_bytes", b.argument, n}
		}
		if total > b.argv {
			return toolcontract.ProducerResult{}, &PredicateLimitError{"argv_bytes", b.argv, total}
		}
	}
	if len(args) == 0 {
		return predicateResult(1), nil
	}
	p := predicateParser{args: args, bounds: b}
	expression, err := p.expression(0, 1)
	if err == nil && p.pos != len(args) {
		err = errPredicateUsage
	}
	if err != nil {
		return predicateFailure(err)
	}
	value, err := r.evalArgv(ctx, invocation, expression)
	return predicateOutcome(value, err)
}
func predicateFailure(err error) (toolcontract.ProducerResult, error) {
	if errors.Is(err, errPredicateUsage) {
		return predicateResult(2), nil
	}
	return toolcontract.ProducerResult{}, err
}
func predicateOutcome(value bool, err error) (toolcontract.ProducerResult, error) {
	if err != nil {
		return predicateFailure(err)
	}
	if value {
		return predicateResult(0), nil
	}
	return predicateResult(1), nil
}
func (r PredicateRuntime) evalArgv(ctx context.Context, inv toolcontract.OperationInvocation, e *predicateExpr) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	switch e.op {
	case "word":
		return e.value != "", nil
	case "!":
		v, err := r.evalArgv(ctx, inv, e.left)
		return !v, err
	case "-a", "-o":
		lhs, err := r.evalArgv(ctx, inv, e.left)
		if err != nil {
			return false, err
		}
		if e.op == "-a" && !lhs || e.op == "-o" && lhs {
			return lhs, nil
		}
		return r.evalArgv(ctx, inv, e.right)
	}
	if e.right == nil {
		return r.unary(ctx, inv, e.op, e.left.value)
	}
	return r.compare(ctx, e.op, e.left.value, PredicateValue{{Value: e.right.value, Quoted: true}}, false)
}

func (r PredicateRuntime) unary(ctx context.Context, inv toolcontract.OperationInvocation, op, value string) (bool, error) {
	switch op {
	case "-n":
		return value != "", nil
	case "-z":
		return value == "", nil
	}
	if !predicateUnary(op) {
		return false, errPredicateUsage
	}
	if r.Files == nil {
		return false, errors.New("predicate filesystem authority port unavailable")
	}
	return r.Files.TestPath(ctx, inv, op, value)
}
