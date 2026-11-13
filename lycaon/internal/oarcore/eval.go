package oarcore

// Evaluation of an already type-checked condition.
//
// [OAR-FACT-8] Condition evaluation is free of side-effects: nothing here
// mutates state and nothing performs input or output. Every state change an
// engine makes is one the rule declared through `on_fire`.
//
// The only failures reachable at run time are the three the specification names:
// an absent map key or an out-of-range index, division or modulo
// by zero, and 64-bit integer overflow. Each
// raises, and the rule is then handled per its `on_error` ([OAR-OPS-2]).

import (
	"fmt"
	"math"
	"strings"
)

// Raise is a run-time condition failure. A rule whose condition raises is a rule
// that cannot be evaluated, and is handled per its on_error.
type Raise struct{ Message string }

func (e *Raise) Error() string { return e.Message }

func raise(format string, args ...any) error {
	return &Raise{Message: fmt.Sprintf(format, args...)}
}

// value is a run-time value of the condition language. The Go representations
// are fixed: bool, int64, float64, string, []string, []map[string]any (a
// list<map>), and map[string]any (a map or a map<string,string>). There is no
// null: every declared fact has a value at every occurrence
// ([OAR-FACT-25]).
type value any

// zeroValue is the value of a declared type the occurrence did not report
// ([OAR-FACT-25]); an unreported fact never raises.
func zeroValue(t FactType) value {
	switch t {
	case TypeBool:
		return false
	case TypeInt:
		return int64(0)
	case TypeDouble:
		return float64(0)
	case TypeString:
		return ""
	case TypeListString:
		return []string{}
	case TypeListMap:
		return []map[string]any{}
	case TypeMap:
		return map[string]any{}
	}
	return nil
}

// evalContext carries the fact values and observation functions one occurrence
// publishes to one rule's condition.
type evalContext struct {
	facts     map[string]value
	functions map[string]func(value) (value, error)
}

func evalNode(n *node, ctx *evalContext) (value, error) {
	switch n.kind {
	case nodeBool:
		return n.boolValue, nil
	case nodeInt:
		return n.intValue, nil
	case nodeDouble:
		return n.doubleValue, nil
	case nodeString:
		return n.strValue, nil
	case nodeList:
		return evalList(n, ctx)
	case nodeIdent:
		v, produced := ctx.facts[n.strValue]
		if !produced {
			return selectMember(n.strValue, ctx)
		}
		return v, nil
	case nodeCall:
		return evalCall(n, ctx)
	case nodeIndex:
		return evalIndex(n, ctx)
	case nodeUnary:
		return evalUnary(n, ctx)
	case nodeTernary:
		// Only the taken branch is evaluated.
		cond, err := evalNode(n.left, ctx)
		if err != nil {
			return nil, err
		}
		taken, err := asBool(cond)
		if err != nil {
			return nil, err
		}
		if taken {
			return evalNode(n.right, ctx)
		}
		return evalNode(n.third, ctx)
	case nodeBinary:
		return evalBinary(n, ctx)
	}
	return nil, raise("unreachable node kind")
}

// evalList builds a list literal. The checker already proved every member has
// the same type, and that the type is string or map.
func evalList(n *node, ctx *evalContext) (value, error) {
	if len(n.items) == 0 {
		return nil, raise("the empty list literal has no inferable type")
	}
	first, err := evalNode(n.items[0], ctx)
	if err != nil {
		return nil, err
	}
	if _, isString := first.(string); isString {
		out := make([]string, 0, len(n.items))
		out = append(out, first.(string))
		for _, item := range n.items[1:] {
			v, err := evalNode(item, ctx)
			if err != nil {
				return nil, err
			}
			s, ok := v.(string)
			if !ok {
				return nil, raise("a list literal member is not a string")
			}
			out = append(out, s)
		}
		return out, nil
	}
	out := make([]map[string]any, 0, len(n.items))
	m, ok := first.(map[string]any)
	if !ok {
		return nil, raise("a list literal member is neither a string nor a map")
	}
	out = append(out, m)
	for _, item := range n.items[1:] {
		v, err := evalNode(item, ctx)
		if err != nil {
			return nil, err
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, raise("a list literal member is neither a string nor a map")
		}
		out = append(out, m)
	}
	return out, nil
}

func evalCall(n *node, ctx *evalContext) (value, error) {
	name := n.strValue
	if name == "size" {
		v, err := evalNode(n.items[0], ctx)
		if err != nil {
			return nil, err
		}
		return sizeOf(v)
	}
	if isStringBuiltin(name) {
		s, err := evalNode(n.items[0], ctx)
		if err != nil {
			return nil, err
		}
		needle, err := evalNode(n.items[1], ctx)
		if err != nil {
			return nil, err
		}
		hay := []rune(s.(string))
		pat := []rune(needle.(string))
		switch name {
		case "starts_with":
			return startsWithRunes(hay, pat), nil
		case "ends_with":
			return endsWithRunes(hay, pat), nil
		default:
			return containsRunes(hay, pat), nil
		}
	}
	fn, produced := ctx.functions[name]
	if !produced {
		return nil, raise("observation function %s was not produced", name)
	}
	arg, err := evalNode(n.items[0], ctx)
	if err != nil {
		return nil, err
	}
	return fn(arg)
}

func evalIndex(n *node, ctx *evalContext) (value, error) {
	target, err := evalNode(n.left, ctx)
	if err != nil {
		return nil, err
	}
	idx, err := evalNode(n.right, ctx)
	if err != nil {
		return nil, err
	}
	switch t := target.(type) {
	case []string:
		i, ok := idx.(int64)
		// Out of range raises, and a negative index
		// is out of range like any other.
		if !ok || i < 0 || i >= int64(len(t)) {
			return nil, raise("list index %v is out of range", idx)
		}
		return t[i], nil
	case []map[string]any:
		i, ok := idx.(int64)
		if !ok || i < 0 || i >= int64(len(t)) {
			return nil, raise("list index %v is out of range", idx)
		}
		return t[i], nil
	case []any:
		i, ok := idx.(int64)
		if !ok || i < 0 || i >= int64(len(t)) {
			return nil, raise("list index %v is out of range", idx)
		}
		return t[i], nil
	case map[string]any:
		key, ok := idx.(string)
		if !ok {
			return nil, raise("a map is indexed by a string")
		}
		// An absent key raises.
		v, present := t[key]
		if !present {
			return nil, raise("map key %q is absent", key)
		}
		return normalizeMember(v), nil
	}
	return nil, raise("the index operator was applied to a scalar")
}

func evalUnary(n *node, ctx *evalContext) (value, error) {
	v, err := evalNode(n.left, ctx)
	if err != nil {
		return nil, err
	}
	if n.op == "!" {
		b, err := asBool(v)
		if err != nil {
			return nil, err
		}
		return !b, nil
	}
	switch x := v.(type) {
	case int64:
		if x == math.MinInt64 {
			return nil, raise("negation overflows a 64-bit signed integer")
		}
		return -x, nil
	case float64:
		return -x, nil
	}
	return nil, raise("unary - was applied to a non-number")
}

func evalBinary(n *node, ctx *evalContext) (value, error) {
	// && and || short-circuit: the right operand is not evaluated
	// when the left decides the result. A condition can therefore guard an
	// expensive or partial sub-expression behind a cheap one.
	if n.op == "&&" || n.op == "||" {
		left, err := evalNode(n.left, ctx)
		if err != nil {
			return nil, err
		}
		l, err := asBool(left)
		if err != nil {
			return nil, err
		}
		if l == (n.op == "||") {
			return l, nil
		}
		right, err := evalNode(n.right, ctx)
		if err != nil {
			return nil, err
		}
		return asBool(right)
	}

	l, err := evalNode(n.left, ctx)
	if err != nil {
		return nil, err
	}
	r, err := evalNode(n.right, ctx)
	if err != nil {
		return nil, err
	}

	switch n.op {
	case "in":
		return evalIn(l, r)
	case "==", "!=":
		if !comparable(l, r) {
			return nil, raise("a selected member of type %T was compared with %T", l, r)
		}
		equal := valuesEqual(l, r)
		return equal == (n.op == "=="), nil
	case "<", "<=", ">", ">=":
		cmp, err := compareValues(l, r)
		if err != nil {
			return nil, err
		}
		switch n.op {
		case "<":
			return cmp < 0, nil
		case "<=":
			return cmp <= 0, nil
		case ">":
			return cmp > 0, nil
		default:
			return cmp >= 0, nil
		}
	case "+":
		return evalAdd(l, r)
	case "-":
		return evalSub(l, r)
	case "*":
		return evalMul(l, r)
	case "/":
		return evalDiv(l, r)
	case "%":
		return evalMod(l, r)
	}
	return nil, raise("unreachable operator %s", n.op)
}

func evalIn(l, r value) (value, error) {
	needle, ok := l.(string)
	if !ok {
		return nil, raise("in takes a string on the left")
	}
	switch t := r.(type) {
	case []string:
		for _, s := range t {
			if s == needle {
				return true, nil
			}
		}
		return false, nil
	case map[string]any:
		// Against a map, `in` tests key presence.
		_, present := t[needle]
		return present, nil
	}
	return nil, raise("in was applied to a scalar")
}

func evalAdd(l, r value) (value, error) {
	switch a := l.(type) {
	case string:
		return a + r.(string), nil
	case int64:
		b := r.(int64)
		sum := a + b
		// Integer arithmetic that overflows 64 bits raises rather
		// than wraps.
		if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
			return nil, raise("addition overflows a 64-bit signed integer")
		}
		return sum, nil
	case float64:
		return finiteDouble(a + r.(float64))
	}
	return nil, raise("+ was applied to an unsupported operand")
}

func evalSub(l, r value) (value, error) {
	switch a := l.(type) {
	case int64:
		b := r.(int64)
		diff := a - b
		if (b < 0 && diff < a) || (b > 0 && diff > a) {
			return nil, raise("subtraction overflows a 64-bit signed integer")
		}
		return diff, nil
	case float64:
		return finiteDouble(a - r.(float64))
	}
	return nil, raise("- was applied to an unsupported operand")
}

func evalMul(l, r value) (value, error) {
	switch a := l.(type) {
	case int64:
		b := r.(int64)
		if a == 0 || b == 0 {
			return int64(0), nil
		}
		product := a * b
		if product/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
			return nil, raise("multiplication overflows a 64-bit signed integer")
		}
		return product, nil
	case float64:
		return finiteDouble(a * r.(float64))
	}
	return nil, raise("* was applied to an unsupported operand")
}

func evalDiv(l, r value) (value, error) {
	switch a := l.(type) {
	case int64:
		b := r.(int64)
		// Division by zero raises.
		if b == 0 {
			return nil, raise("division by zero")
		}
		if a == math.MinInt64 && b == -1 {
			return nil, raise("division overflows a 64-bit signed integer")
		}
		// Integer division truncates toward zero, so -7 / 2 is -3,
		// which is what Go's own integer division already does.
		return a / b, nil
	case float64:
		b := r.(float64)
		if b == 0 {
			return nil, raise("division by zero")
		}
		return finiteDouble(a / b)
	}
	return nil, raise("/ was applied to an unsupported operand")
}

func evalMod(l, r value) (value, error) {
	a, ok := l.(int64)
	if !ok {
		return nil, raise("%% takes two int operands")
	}
	b, ok := r.(int64)
	if !ok {
		return nil, raise("%% takes two int operands")
	}
	// Modulo by zero raises.
	if b == 0 {
		return nil, raise("modulo by zero")
	}
	if a == math.MinInt64 && b == -1 {
		return int64(0), nil
	}
	// -7 % 2 is -1, which is what Go's own remainder already gives.
	return a % b, nil
}

// valuesEqual compares two values. an int is promoted to double
// for a mixed comparison only. two aggregates never reach here:
// the type checker rejects that comparison at load.
func valuesEqual(l, r value) bool {
	li, lIsInt := l.(int64)
	rd, rIsDouble := r.(float64)
	if lIsInt && rIsDouble {
		return float64(li) == rd
	}
	ld, lIsDouble := l.(float64)
	ri, rIsInt := r.(int64)
	if lIsDouble && rIsInt {
		return ld == float64(ri)
	}
	switch a := l.(type) {
	case bool:
		b, ok := r.(bool)
		return ok && a == b
	case int64:
		b, ok := r.(int64)
		return ok && a == b
	case float64:
		b, ok := r.(float64)
		return ok && a == b
	case string:
		b, ok := r.(string)
		return ok && a == b
	}
	return false
}

// compareValues orders two values. String comparison is by Unicode code point.
func compareValues(l, r value) (int, error) {
	if ls, ok := l.(string); ok {
		rs, ok := r.(string)
		if !ok {
			return 0, raise("a string was compared with a non-string")
		}
		return compareCodePoints(ls, rs), nil
	}
	li, lIsInt := l.(int64)
	ri, rIsInt := r.(int64)
	if lIsInt && rIsInt {
		switch {
		case li < ri:
			return -1, nil
		case li > ri:
			return 1, nil
		default:
			return 0, nil
		}
	}
	a, err := asFloat(l)
	if err != nil {
		return 0, err
	}
	b, err := asFloat(r)
	if err != nil {
		return 0, err
	}
	switch {
	case a < b:
		return -1, nil
	case a > b:
		return 1, nil
	default:
		return 0, nil
	}
}

func asFloat(v value) (float64, error) {
	switch x := v.(type) {
	case int64:
		return float64(x), nil
	case float64:
		return x, nil
	}
	return 0, raise("an ordering comparison was applied to a non-number")
}

// compareCodePoints compares two strings by Unicode code point, as the standard
// states the rule; Go's byte order agrees only for valid UTF-8.
func compareCodePoints(a, b string) int {
	x := []rune(a)
	y := []rune(b)
	n := len(x)
	if len(y) < n {
		n = len(y)
	}
	for i := 0; i < n; i++ {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(x) < len(y):
		return -1
	case len(x) > len(y):
		return 1
	default:
		return 0
	}
}

// sizeOf counts members, or on a string the number of Unicode code points.
func sizeOf(v value) (value, error) {
	switch t := v.(type) {
	case string:
		return int64(len([]rune(t))), nil
	case []string:
		return int64(len(t)), nil
	case []map[string]any:
		return int64(len(t)), nil
	case map[string]any:
		// A map<string,string> is a map for the purposes of in and
		// size().
		return int64(len(t)), nil
	case []any:
		return int64(len(t)), nil
	}
	return nil, raise("size() was applied to a scalar")
}

// [OAR-FACT-17] A string is a sequence of Unicode code points,
// and the three string built-ins compare over that sequence with no
// normalisation, no case folding, and no locale. They run over runes, so an
// astral-plane character is one member and a partial code point never matches.

func prefixAt(hay, needle []rune, at int) bool {
	for i := range needle {
		if at+i >= len(hay) || hay[at+i] != needle[i] {
			return false
		}
	}
	return true
}

// startsWithRunes reports whether hay begins with pat. An empty prefix matches
// every string.
func startsWithRunes(hay, pat []rune) bool {
	if len(pat) > len(hay) {
		return false
	}
	return prefixAt(hay, pat, 0)
}

// endsWithRunes reports whether hay ends with pat. An empty suffix matches every
// string.
func endsWithRunes(hay, pat []rune) bool {
	if len(pat) > len(hay) {
		return false
	}
	return prefixAt(hay, pat, len(hay)-len(pat))
}

// containsRunes reports whether pat occurs anywhere in hay. An empty substring
// matches every string.
func containsRunes(hay, pat []rune) bool {
	if len(pat) > len(hay) {
		return false
	}
	for at := 0; at+len(pat) <= len(hay); at++ {
		if prefixAt(hay, pat, at) {
			return true
		}
	}
	return false
}

func finiteDouble(v float64) (value, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, raise("non-finite double arithmetic result")
	}
	return v, nil
}

// asBool reads a value a boolean context requires. Only a selected map member
// can arrive with another type, and that is a fact-provider failure
// ([OAR-FACT-22], [OAR-FACT-23]).
func asBool(v value) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, raise("a selected member of type %T stands where bool is required", v)
	}
	return b, nil
}

// comparable reports whether == may compare two runtime values: the same
// scalar kind, or two numbers.
func comparable(l, r value) bool {
	switch l.(type) {
	case int64, float64:
		switch r.(type) {
		case int64, float64:
			return true
		}
		return false
	case bool:
		_, ok := r.(bool)
		return ok
	case string:
		_, ok := r.(string)
		return ok
	}
	return false
}

// normalizeMember gives a selected map member the engine's runtime kinds:
// integers as int64, lists of strings as []string.
func normalizeMember(v any) value {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint32:
		return int64(x)
	case float32:
		return float64(x)
	case []any:
		strs := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return x
			}
			strs = append(strs, s)
		}
		return strs
	}
	return v
}

// selectMember reads the member a dotted name selects from a map<string, dyn>
// fact ([OAR-FACT-22]). An absent member reads as the empty string; a member
// holding anything but a string fails like a provider ([OAR-FACT-23]).
func selectMember(name string, ctx *evalContext) (value, error) {
	at := strings.LastIndex(name, ".")
	if at > 0 {
		if m, ok := ctx.facts[name[:at]].(map[string]any); ok {
			member, present := m[name[at+1:]]
			if !present {
				return "", nil
			}
			if s, isString := member.(string); isString {
				return s, nil
			}
			return nil, raise("[OAR-FACT-23] %s holds %T where a string was assumed", name, member)
		}
	}
	return nil, raise("fact %s was not produced", name)
}
