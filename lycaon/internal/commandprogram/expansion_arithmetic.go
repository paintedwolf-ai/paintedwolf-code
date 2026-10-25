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
	switch n.Operator {
	case "+":
		return left + right, nil
	case "-":
		return left - right, nil
	case "*":
		return left * right, nil
	case "/", "%":
		if right == 0 {
			return 0, invalidValue(n.Span, "a non-zero divisor")
		}
		if left == math.MinInt64 && right == -1 {
			if n.Operator == "/" {
				return left, nil
			}
			return 0, nil
		}
		if n.Operator == "/" {
			return left / right, nil
		}
		return left % right, nil
	case "**":
		if right < 0 {
			return 0, invalidValue(n.Span, "a non-negative exponent")
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
		return left << (right & 63), nil
	case ">>":
		return left >> (right & 63), nil
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
	return 0, &ExpansionError{Code: codeSyntaxUnsupported, Span: n.Span, Expected: "a version 1 arithmetic operator"}
}

// arithmeticOperand reads an integer literal or a variable holding one; an
// unset or empty value is zero. Values are never evaluated as expressions.
func (r *expansionRun) arithmeticOperand(ctx context.Context, n commandsurface.ArithmeticOperand) (int64, error) {
	text, err := r.expander.ExpandScalar(ctx, r.scope, n.Word)
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
	// Preserve the low 64 bits without an out-of-range unsigned conversion.
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
