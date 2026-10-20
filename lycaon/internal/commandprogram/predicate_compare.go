package commandprogram

import (
	"context"
	"errors"
	"strings"
)

func (r PredicateRuntime) compare(ctx context.Context, op, lhs string, rhs PredicateValue, patterns bool) (bool, error) {
	switch op {
	case "=", "==", "!=":
		var equal bool
		if patterns {
			if r.Patterns == nil {
				return false, errors.New("predicate pattern port unavailable")
			}
			var err error
			equal, err = r.Patterns.MatchPredicate(ctx, "glob", lhs, rhs)
			if err != nil {
				return false, err
			}
		} else {
			right, err := r.text(rhs)
			if err != nil {
				return false, err
			}
			equal = lhs == right
		}
		if op == "!=" {
			equal = !equal
		}
		return equal, nil
	case "=~":
		if r.Patterns == nil {
			return false, errors.New("predicate pattern port unavailable")
		}
		return r.Patterns.MatchPredicate(ctx, "ere", lhs, rhs)
	}
	right, err := r.text(rhs)
	if err != nil {
		return false, err
	}
	a, ok := decimalOperand(lhs)
	if !ok {
		return false, errPredicateUsage
	}
	b, ok := decimalOperand(right)
	if !ok {
		return false, errPredicateUsage
	}
	// Compare arbitrary precision decimal operands without evaluating arithmetic or
	// allocating magnitude-sized integers. Leading zeroes remain decimal.
	order := compareDecimal(a, b)
	switch op {
	case "-eq":
		return order == 0, nil
	case "-ne":
		return order != 0, nil
	case "-lt":
		return order < 0, nil
	case "-le":
		return order <= 0, nil
	case "-gt":
		return order > 0, nil
	case "-ge":
		return order >= 0, nil
	}
	return false, errPredicateUsage
}

type decimalValue struct {
	digits   string
	negative bool
}

func decimalOperand(value string) (decimalValue, bool) {
	negative := false
	if len(value) > 0 && (value[0] == '-' || value[0] == '+') {
		negative = value[0] == '-'
		value = value[1:]
	}
	if value == "" {
		return decimalValue{}, false
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return decimalValue{}, false
		}
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		value = "0"
		negative = false
	}
	return decimalValue{value, negative}, true
}
func compareDecimal(a, b decimalValue) int {
	if a.negative != b.negative {
		if a.negative {
			return -1
		}
		return 1
	}
	order := 0
	if len(a.digits) < len(b.digits) {
		order = -1
	} else if len(a.digits) > len(b.digits) {
		order = 1
	} else {
		order = strings.Compare(a.digits, b.digits)
	}
	if a.negative {
		return -order
	}
	return order
}
