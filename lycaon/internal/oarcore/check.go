package oarcore

// Appendix A, section A.2: the type rules of the condition language.
//
// Every condition is fully typed at load ([OAR-FACT-3]). Selecting a member of
// a map<string, dyn> yields a dyn value ([OAR-FACT-22]): it takes the type its
// use requires, a use that fixes no type is rejected at load, and evaluation
// checks the member's actual type, so a mismatch is a fact-provider failure
// ([OAR-FACT-23]) rather than a coercion.

import (
	"fmt"
	"strings"
)

// TypeError is a condition that does not type-check against the declared
// environment. It names the offending construct and its offset.
type TypeError struct {
	Message string
	Offset  int
}

func (e *TypeError) Error() string {
	return fmt.Sprintf("%s at offset %d", e.Message, e.Offset)
}

func typeErr(offset int, format string, args ...any) error {
	return &TypeError{Message: fmt.Sprintf(format, args...), Offset: offset}
}

// typeDyn is the type of a selected map member. It is never declarable.
const typeDyn FactType = "dyn"

func isNumeric(t FactType) bool { return t == TypeInt || t == TypeDouble || t == typeDyn }

func isAggregate(t FactType) bool {
	return t == TypeListString || t == TypeListMap || t == TypeMap
}

// is reports whether t can stand where want is required: itself, or a dyn
// member checked at evaluation.
func is(t, want FactType) bool { return t == want || t == typeDyn }

func sameOrPromotable(a, b FactType) bool {
	if a == b || a == typeDyn || b == typeDyn {
		return true
	}
	return isNumeric(a) && isNumeric(b)
}

// checker type-checks one condition against a declared environment, collecting
// the fact and function names it references.
type checker struct {
	env *Environment
	// reachable is what the rule may name: the core tier is always reachable, and
	// anything else only through `requires`. A reference outside it is rejected
	// under [OAR-FACT-20], so portability is computable from the document alone.
	reachable map[string]bool
	refs      map[string]bool
}

// checkCondition type-checks node and returns its type and the names it
// references.
func checkCondition(n *node, env *Environment, reachable map[string]bool) (FactType, map[string]bool, error) {
	c := &checker{env: env, reachable: reachable, refs: map[string]bool{}}
	t, err := c.walk(n)
	if err != nil {
		return "", nil, err
	}
	return t, c.refs, nil
}

func (c *checker) walk(n *node) (FactType, error) {
	switch n.kind {
	case nodeBool:
		return TypeBool, nil
	case nodeInt:
		return TypeInt, nil
	case nodeDouble:
		return TypeDouble, nil
	case nodeString:
		return TypeString, nil
	case nodeList:
		return c.list(n)
	case nodeIdent:
		return c.ident(n)
	case nodeCall:
		return c.call(n)
	case nodeIndex:
		return c.index(n)
	case nodeUnary:
		return c.unary(n)
	case nodeTernary:
		return c.ternary(n)
	case nodeBinary:
		return c.binary(n)
	}
	return "", typeErr(n.offset, "unreachable node kind")
}

// list types a list literal. [OAR-EXPR-21] a list literal is list<string> when
// every member is a string and list<map> when every member is a map, and is
// rejected otherwise: members merely agreeing is necessary but not sufficient,
// because [OAR-FACT-22] defines no other list type. The empty literal is
// rejected because its type cannot be inferred.
func (c *checker) list(n *node) (FactType, error) {
	if len(n.items) == 0 {
		return "", typeErr(n.offset, "[OAR-EXPR-21] the empty list literal [] has no inferable type")
	}
	types := make([]FactType, 0, len(n.items))
	for _, item := range n.items {
		t, err := c.walk(item)
		if err != nil {
			return "", err
		}
		types = append(types, t)
	}
	allString, allMap := true, true
	for _, t := range types {
		if t != TypeString {
			allString = false
		}
		if t != TypeMap {
			allMap = false
		}
	}
	if allString {
		return TypeListString, nil
	}
	if allMap {
		return TypeListMap, nil
	}
	seen := map[FactType]bool{}
	var distinct []string
	for _, t := range types {
		if !seen[t] {
			seen[t] = true
			distinct = append(distinct, string(t))
		}
	}
	return "", typeErr(n.offset,
		"[OAR-EXPR-21] a list literal is list<string> or list<map>, given members of type %s",
		strings.Join(distinct, ", "))
}

func (c *checker) ident(n *node) (FactType, error) {
	fact, declared := c.env.Facts[n.strValue]
	if !declared {
		if _, isFn := c.env.Functions[n.strValue]; isFn {
			return "", typeErr(n.offset, "[OAR-FACT-3] %s is an observation function and must be called", n.strValue)
		}
		// [OAR-FACT-3] A mistyped fact name is a load failure, never a runtime
		// default. [OAR-EXPR-2] a dotted identifier is a single name, so an author
		// reaching for field selection lands here and gets a hint.
		return "", typeErr(n.offset, "unknown identifier %s%s", n.strValue, c.selectionHint(n.strValue))
	}
	if err := c.requireReachable(n.strValue, fact.Tier, n.offset); err != nil {
		return "", err
	}
	c.refs[n.strValue] = true
	return fact.Type, nil
}

func (c *checker) call(n *node) (FactType, error) {
	name := n.strValue
	// [OAR-EXPR-16] The language defines exactly four built-ins: size, and the
	// three string operations. Everything else is a declared observation
	// function, which is subject to the tier rules.
	if name == "size" {
		if len(n.items) != 1 {
			return "", typeErr(n.offset, "[OAR-EXPR-16] size() takes exactly one argument, given %d", len(n.items))
		}
		t, err := c.walk(n.items[0])
		if err != nil {
			return "", err
		}
		if t != TypeString && t != TypeListString && t != TypeListMap && t != TypeMap && t != typeDyn {
			return "", typeErr(n.offset, "[OAR-EXPR-16] size() takes a string, a list, or a map, given %s", t)
		}
		return TypeInt, nil
	}
	// [OAR-EXPR-16], [OAR-EXPR-22] starts_with, ends_with, and contains are
	// (string, string) -> bool, compared by Unicode code point with no
	// normalisation, no case folding, and no locale.
	if isStringBuiltin(name) {
		if len(n.items) != 2 {
			return "", typeErr(n.offset, "[OAR-EXPR-16] %s() takes exactly two arguments, given %d", name, len(n.items))
		}
		a, err := c.walk(n.items[0])
		if err != nil {
			return "", err
		}
		b, err := c.walk(n.items[1])
		if err != nil {
			return "", err
		}
		if !is(a, TypeString) || !is(b, TypeString) {
			return "", typeErr(n.offset, "[OAR-EXPR-16] %s() takes two string arguments, given %s and %s", name, a, b)
		}
		return TypeBool, nil
	}

	fn, declared := c.env.Functions[name]
	if !declared {
		if _, isFact := c.env.Facts[name]; isFact {
			return "", typeErr(n.offset, "[OAR-FACT-3] %s is a fact, not an observation function", name)
		}
		return "", typeErr(n.offset, "unknown identifier %s", name)
	}
	if err := c.requireReachable(name, fn.Tier, n.offset); err != nil {
		return "", err
	}
	c.refs[name] = true
	if len(n.items) != 1 {
		return "", typeErr(n.offset,
			"[OAR-FACT-9] observation function %s takes exactly one argument, given %d", name, len(n.items))
	}
	// Observation function arguments are ordinary expressions.
	argType, err := c.walk(n.items[0])
	if err != nil {
		return "", err
	}
	if !is(argType, fn.Sig.Arg) {
		return "", typeErr(n.offset,
			"[OAR-FACT-9] observation function %s takes %s, given %s", name, fn.Sig.Arg, argType)
	}
	return fn.Sig.Ret, nil
}

// index types the index operator exhaustively ([OAR-EXPR-13]).
func (c *checker) index(n *node) (FactType, error) {
	target, err := c.walk(n.left)
	if err != nil {
		return "", err
	}
	idx, err := c.walk(n.right)
	if err != nil {
		return "", err
	}
	if target == TypeMap && is(idx, TypeString) {
		return typeDyn, nil
	}
	if target == TypeListString && is(idx, TypeInt) {
		return TypeString, nil
	}
	if target == TypeListMap && is(idx, TypeInt) {
		return TypeMap, nil
	}
	if target == typeDyn && (is(idx, TypeString) || is(idx, TypeInt)) {
		return typeDyn, nil
	}
	return "", typeErr(n.offset,
		"[OAR-EXPR-13] the index operator does not accept %s indexed by %s", target, idx)
}

func (c *checker) unary(n *node) (FactType, error) {
	t, err := c.walk(n.left)
	if err != nil {
		return "", err
	}
	if n.op == "!" {
		// [OAR-EXPR-8] ! takes and produces bool.
		if !is(t, TypeBool) {
			return "", typeErr(n.offset, "[OAR-EXPR-8] ! takes bool, given %s", t)
		}
		return TypeBool, nil
	}
	if t == typeDyn {
		return "", typeErr(n.offset, "[OAR-FACT-22] unary - fixes no type for the selected member %s", describe(n.left))
	}
	if !isNumeric(t) {
		return "", typeErr(n.offset, "[OAR-EXPR-9] unary - takes int or double, given %s", t)
	}
	return t, nil
}

// ternary types the conditional. [OAR-EXPR-8] the condition is bool and the
// branches share a type.
func (c *checker) ternary(n *node) (FactType, error) {
	cond, err := c.walk(n.left)
	if err != nil {
		return "", err
	}
	if !is(cond, TypeBool) {
		return "", typeErr(n.offset, "[OAR-EXPR-8] the ternary condition must be bool, given %s", cond)
	}
	a, err := c.walk(n.right)
	if err != nil {
		return "", err
	}
	b, err := c.walk(n.third)
	if err != nil {
		return "", err
	}
	switch {
	case a == b:
		return a, nil
	case a == typeDyn:
		return b, nil
	case b == typeDyn:
		return a, nil
	}
	return "", typeErr(n.offset, "[OAR-EXPR-8] the ternary branches are %s and %s, which differ", a, b)
}

func (c *checker) binary(n *node) (FactType, error) {
	left, err := c.walk(n.left)
	if err != nil {
		return "", err
	}
	right, err := c.walk(n.right)
	if err != nil {
		return "", err
	}
	op := n.op

	// [OAR-EXPR-8] && and || take and produce bool.
	if op == "&&" || op == "||" {
		if !is(left, TypeBool) || !is(right, TypeBool) {
			return "", typeErr(n.offset, "[OAR-EXPR-8] %s takes bool operands, given %s and %s", op, left, right)
		}
		return TypeBool, nil
	}

	// [OAR-EXPR-12] in takes a string and a list<string>, or a string and a map,
	// and against a map tests key presence.
	if op == "in" {
		if !is(left, TypeString) {
			return "", typeErr(n.offset, "[OAR-EXPR-12] in takes a string on the left, given %s", left)
		}
		if right != TypeListString && right != TypeMap && right != typeDyn {
			return "", typeErr(n.offset, "[OAR-EXPR-12] in takes a list<string> or a map on the right, given %s", right)
		}
		return TypeBool, nil
	}

	// [OAR-EXPR-11] == and != take two operands of the same type, or one int and
	// one double. A comparison between unrelated types is rejected at load rather
	// than returning false.
	if op == "==" || op == "!=" {
		// [OAR-EXPR-21] Equality over two aggregates is rejected at load.
		if isAggregate(left) && isAggregate(right) {
			return "", typeErr(n.offset,
				"[OAR-EXPR-21] %s does not compare two aggregate values, given %s and %s", op, left, right)
		}
		if !sameOrPromotable(left, right) {
			return "", typeErr(n.offset, "[OAR-EXPR-11] %s compares unrelated types %s and %s", op, left, right)
		}
		return TypeBool, nil
	}

	// [OAR-EXPR-10] Ordering comparisons take two int, two double, one of each,
	// or two string.
	if op == "<" || op == "<=" || op == ">" || op == ">=" {
		if left == typeDyn && right == typeDyn {
			return "", typeErr(n.offset, "[OAR-FACT-22] %s fixes no type for two selected members", op)
		}
		if is(left, TypeString) && is(right, TypeString) {
			return TypeBool, nil
		}
		if isNumeric(left) && isNumeric(right) {
			return TypeBool, nil
		}
		return "", typeErr(n.offset,
			"[OAR-EXPR-10] %s takes two numbers or two strings, given %s and %s", op, left, right)
	}

	// [OAR-EXPR-9] Arithmetic takes two int or two double; % takes two int; +
	// additionally concatenates two strings.
	if left == typeDyn && right == typeDyn {
		return "", typeErr(n.offset, "[OAR-FACT-22] %s fixes no type for two selected members", op)
	}
	if left == typeDyn {
		left = right
	}
	if right == typeDyn {
		right = left
	}
	if op == "%" {
		if left != TypeInt || right != TypeInt {
			return "", typeErr(n.offset, "[OAR-EXPR-9] %% takes two int operands, given %s and %s", left, right)
		}
		return TypeInt, nil
	}
	if op == "+" && left == TypeString && right == TypeString {
		return TypeString, nil
	}
	if left == TypeInt && right == TypeInt {
		return TypeInt, nil
	}
	if left == TypeDouble && right == TypeDouble {
		return TypeDouble, nil
	}
	return "", typeErr(n.offset,
		"[OAR-EXPR-9] %s takes two int or two double operands, given %s and %s", op, left, right)
}

func (c *checker) requireReachable(name string, tier Tier, offset int) error {
	if tier == TierCore || c.reachable[name] {
		return nil
	}
	// [OAR-FACT-20] Reject a reference to a non-core capability the rule does not
	// reach through requires.
	return typeErr(offset,
		"[OAR-FACT-20] %s is outside the core tier and the rule does not reach it through requires", name)
}

// selectionHint names the one mistake this grammar invites: writing
// `tool_args.path` and expecting field selection ([OAR-EXPR-2],
// [OAR-EXPR-19]). The whole dotted string is one name, and the member is reached
// with `in` plus a typed accessor.
func (c *checker) selectionHint(name string) string {
	at := strings.LastIndex(name, ".")
	if at == -1 {
		return ""
	}
	prefix, member := name[:at], name[at+1:]
	decl, declared := c.env.Facts[prefix]
	if !declared {
		return ""
	}
	if decl.Type == TypeMap {
		return fmt.Sprintf(
			" — a dotted identifier is a single name and there is no field-selection operator;"+
				" select the member with %s[%q]", prefix, member)
	}
	return fmt.Sprintf(
		" — a dotted identifier is a single name and there is no field-selection operator; %s is a %s",
		prefix, decl.Type)
}

// describe names the operand a diagnostic is about, when it has a name.
func describe(n *node) string {
	switch n.kind {
	case nodeIdent:
		return n.strValue
	case nodeCall:
		return n.strValue + "()"
	default:
		return "the operand"
	}
}

func describeKind(k nodeKind) string {
	switch k {
	case nodeBool:
		return "bool"
	case nodeInt:
		return "int"
	case nodeDouble:
		return "double"
	case nodeString:
		return "string"
	case nodeList:
		return "list"
	case nodeIdent:
		return "ident"
	case nodeCall:
		return "call"
	case nodeIndex:
		return "index"
	case nodeUnary:
		return "unary"
	case nodeBinary:
		return "binary"
	case nodeTernary:
		return "ternary"
	}
	return "unknown"
}
