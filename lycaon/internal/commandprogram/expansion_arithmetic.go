package commandprogram

import (
	"context"
	"math"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// arithmetic evaluates $(( )) with wrapping signed 64-bit integers. && || and
// ?: evaluate only the selected operands.
func (r *expansionRun) arithmetic(ctx context.Context, part commandsurface.ArithmeticPart, quoted bool) error {
	value, err := r.arithmeticValue(ctx, part.Expression)
	if err != nil {
		return err
	}
	return r.emit(ctx, strconv.FormatInt(value, 10), quoted)
}

func (r *expansionRun) arithmeticValue(ctx context.Context, node commandsurface.ArithmeticNode) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	switch n := node.(type) {
	case commandsurface.ArithmeticOperand:
		return r.arithmeticOperand(ctx, n)
	case commandsurface.ArithmeticUnary:
		if n.Operator == "++" || n.Operator == "--" {
			return r.arithmeticIncDec(ctx, n)
		}
		value, err := r.arithmeticValue(ctx, n.Operand)
		if err != nil {
			return 0, err
		}
		switch n.Operator {
		case "-":
			return -value, nil
		case "!":
			return boolInt(value == 0), nil
		case "~":
			return ^value, nil
		}
		return value, nil
	case commandsurface.ArithmeticBinary:
		return r.arithmeticBinary(ctx, n)
	}
	return 0, &ExpansionError{Code: codeSyntaxUnsupported, Span: r.span, Expected: "a version 1 arithmetic expression"}
}

func (r *expansionRun) arithmeticBinary(ctx context.Context, n commandsurface.ArithmeticBinary) (int64, error) {
	if n.Operator == "," {
		if !r.allowArithmeticMutation {
			return 0, &ExpansionError{Code: codeSyntaxUnsupported, Span: n.Span, Expected: "a version 1 arithmetic operator"}
		}
		if _, err := r.arithmeticValue(ctx, n.Left); err != nil {
			return 0, err
		}
		return r.arithmeticValue(ctx, n.Right)
	}
	if baseOp, isAssign := arithmeticAssignBase(n.Operator); isAssign {
		return r.arithmeticAssign(ctx, n, baseOp)
	}
	left, err := r.arithmeticValue(ctx, n.Left)
	if err != nil {
		return 0, err
	}
	switch n.Operator {
	case "&&", "||":
		if (n.Operator == "&&") == (left == 0) {
			return boolInt(left != 0), nil
		}
		right, err := r.arithmeticValue(ctx, n.Right)
		return boolInt(right != 0), err
	case "?":
		branches, ok := n.Right.(commandsurface.ArithmeticBinary)
		if !ok || branches.Operator != ":" {
			return 0, invalidValue(n.Span, "a ? b : c")
		}
		if left != 0 {
			return r.arithmeticValue(ctx, branches.Left)
		}
		return r.arithmeticValue(ctx, branches.Right)
	}
	right, err := r.arithmeticValue(ctx, n.Right)
	if err != nil {
		return 0, err
	}
	return evalArithmeticBinaryOp(n.Operator, left, right, n.Span)
}

func evalArithmeticBinaryOp(op string, left, right int64, span toolcontract.SourceSpan) (int64, error) {
	switch op {
	case "+":
		return left + right, nil
	case "-":
		return left - right, nil
	case "*":
		return left * right, nil
	case "/", "%":
		if right == 0 {
			return 0, invalidValue(span, "a non-zero divisor")
		}
		if left == math.MinInt64 && right == -1 {
			if op == "/" {
				return left, nil
			}
			return 0, nil
		}
		if op == "/" {
			return left / right, nil
		}
		return left % right, nil
	case "**":
		if right < 0 {
			return 0, invalidValue(span, "a non-negative exponent")
		}
		result := int64(1)
		for base := left; right > 0; right >>= 1 {
			if right&1 == 1 {
				result *= base
			}
			base *= base
		}
		return result, nil
	case "<<":
		return left << (uint64(right & 63)), nil
	case ">>":
		return left >> (uint64(right & 63)), nil
	case "&":
		return left & right, nil
	case "|":
		return left | right, nil
	case "^":
		return left ^ right, nil
	case "==":
		return boolInt(left == right), nil
	case "!=":
		return boolInt(left != right), nil
	case "<":
		return boolInt(left < right), nil
	case "<=":
		return boolInt(left <= right), nil
	case ">":
		return boolInt(left > right), nil
	case ">=":
		return boolInt(left >= right), nil
	}
	return 0, &ExpansionError{Code: codeSyntaxUnsupported, Span: span, Expected: "a version 1 arithmetic operator"}
}

func arithmeticAssignBase(op string) (string, bool) {
	switch op {
	case "=":
		return "", true
	case "+=", "-=", "*=", "/=", "%=", "<<=", ">>=", "&=", "|=", "^=":
		return strings.TrimSuffix(op, "="), true
	default:
		return "", false
	}
}

func (r *expansionRun) arithmeticIncDec(ctx context.Context, n commandsurface.ArithmeticUnary) (int64, error) {
	if !r.allowArithmeticMutation {
		return 0, &ExpansionError{Code: codeSyntaxUnsupported, Span: n.Span, Expected: "a version 1 arithmetic expression"}
	}
	name, index, current, err := r.arithmeticTarget(ctx, n.Operand, n.Span, true)
	if err != nil {
		return 0, err
	}
	next := current + 1
	if n.Operator == "--" {
		next = current - 1
	}
	if err := assignScopeValue(ctx, r.scope, name, index, strconv.FormatInt(next, 10), n.Span); err != nil {
		return 0, err
	}
	if n.Postfix {
		return current, nil
	}
	return next, nil
}

func (r *expansionRun) arithmeticAssign(ctx context.Context, n commandsurface.ArithmeticBinary, baseOp string) (int64, error) {
	if !r.allowArithmeticMutation {
		return 0, &ExpansionError{Code: codeSyntaxUnsupported, Span: n.Span, Expected: "a version 1 arithmetic operator"}
	}
	var name string
	var index *int64
	var current, right int64
	var err error
	if baseOp == "" {
		// Plain assignment evaluates the right side before resolving its target
		// subscript and never interprets the previous value as an integer.
		right, err = r.arithmeticValue(ctx, n.Right)
		if err == nil {
			name, index, current, err = r.arithmeticTarget(ctx, n.Left, n.Span, false)
		}
	} else {
		name, index, current, err = r.arithmeticTarget(ctx, n.Left, n.Span, true)
		if err == nil {
			right, err = r.arithmeticValue(ctx, n.Right)
		}
	}
	if err != nil {
		return 0, err
	}
	next := right
	if baseOp != "" {
		next, err = evalArithmeticBinaryOp(baseOp, current, right, n.Span)
		if err != nil {
			return 0, err
		}
	}
	if err := assignScopeValue(ctx, r.scope, name, index, strconv.FormatInt(next, 10), n.Span); err != nil {
		return 0, err
	}
	return next, nil
}

func (r *expansionRun) arithmeticTarget(ctx context.Context, node commandsurface.ArithmeticNode, span toolcontract.SourceSpan, readCurrent bool) (string, *int64, int64, error) {
	operand, ok := node.(commandsurface.ArithmeticOperand)
	if !ok || len(operand.Word.Parts) != 1 {
		return "", nil, 0, invalidValue(span, "an assignable variable")
	}
	switch part := operand.Word.Parts[0].(type) {
	case commandsurface.LiteralPart:
		name := strings.TrimSpace(part.Value)
		if !VariableName(name) || name == "IFS" || name == "PIPESTATUS" {
			return "", nil, 0, invalidValue(span, "an assignable variable")
		}
		if !readCurrent {
			return name, nil, 0, nil
		}
		raw, _ := r.scope.Variable(name)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return name, nil, 0, nil
		}
		val, err := parseArithmeticInteger(raw, span)
		return name, nil, val, err
	case commandsurface.ParameterPart:
		if part.Index == nil || !VariableName(part.Name) || part.Name == "IFS" || part.Name == "PIPESTATUS" {
			return "", nil, 0, invalidValue(span, "an assignable variable")
		}
		idx, err := r.arithmeticValue(ctx, part.Index)
		if err != nil {
			return "", nil, 0, err
		}
		if idx < 0 {
			return "", nil, 0, invalidValue(span, "a non-negative array subscript")
		}
		if !readCurrent {
			return part.Name, &idx, 0, nil
		}
		raw, _ := scopeArrayElement(r.scope, part.Name, idx)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return part.Name, &idx, 0, nil
		}
		val, err := parseArithmeticInteger(raw, span)
		return part.Name, &idx, val, err
	default:
		return "", nil, 0, invalidValue(span, "an assignable variable")
	}
}

// arithmeticOperand reads an integer literal or a variable holding one; an
// unset or empty value is zero. Values are never evaluated as expressions.
func (r *expansionRun) arithmeticOperand(ctx context.Context, n commandsurface.ArithmeticOperand) (int64, error) {
	var text string
	var err error
	if len(n.Word.Parts) == 1 {
		switch part := n.Word.Parts[0].(type) {
		case commandsurface.ParameterPart:
			if part.Index != nil {
				text, err = r.resolveParameterValue(part)
			} else {
				text, err = r.expander.ExpandScalar(ctx, r.scope, n.Word)
			}
		case commandsurface.LiteralPart:
			if !strings.ContainsAny(part.Value, "\\'\"~@") {
				text = part.Value
			} else {
				text, err = r.expander.ExpandScalar(ctx, r.scope, n.Word)
			}
		default:
			text, err = r.expander.ExpandScalar(ctx, r.scope, n.Word)
		}
	} else {
		text, err = r.expander.ExpandScalar(ctx, r.scope, n.Word)
	}
	if err != nil {
		return 0, err
	}
	text = strings.TrimSpace(text)
	if VariableName(text) {
		text, _ = r.scope.Variable(text)
		text = strings.TrimSpace(text)
	}
	if text == "" {
		return 0, nil
	}
	return parseArithmeticInteger(text, n.Span)
}

// parseArithmeticInteger accepts decimal, 0x hexadecimal and leading-zero octal.
func parseArithmeticInteger(text string, span toolcontract.SourceSpan) (int64, error) {
	negative := strings.HasPrefix(text, "-")
	digits := strings.TrimPrefix(strings.TrimPrefix(text, "-"), "+")
	base := 10
	switch {
	case strings.HasPrefix(digits, "0x") || strings.HasPrefix(digits, "0X"):
		base, digits = 16, digits[2:]
	case len(digits) > 1 && digits[0] == '0':
		base, digits = 8, digits[1:]
	}
	value, err := strconv.ParseUint(digits, base, 64)
	if err != nil {
		return 0, invalidValue(span, "an integer arithmetic operand")
	}
	// Reconstruct the signed bit pattern without narrowing an unsigned value
	// outside int64's positive range; arithmetic remains modulo 2^64.
	signed := int64(value & math.MaxInt64)
	if value > math.MaxInt64 {
		signed += math.MinInt64
	}
	if negative {
		return -signed, nil
	}
	return signed, nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
