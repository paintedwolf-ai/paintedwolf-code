package oarcore

// Appendix C, section C.2: the type rules of the condition language. Every
// condition is fully typed at load ([OAR-FACT-3]), and every rejection cites
// the clause it applies ([OAR-EXPR-1]).

import (
	"fmt"
	"strings"
)

// TypeError is a condition that does not type-check against the declared
// environment. It names the offending construct, its offset, and the clause.
type TypeError struct {
	Message string
	Offset  int
	Clause  string
}

func (e *TypeError) Error() string {
	return fmt.Sprintf("%s at offset %d [%s]", e.Message, e.Offset, e.Clause)
}

func typeErr(clause string, offset int, format string, args ...any) error {
	return &TypeError{Message: fmt.Sprintf(format, args...), Offset: offset, Clause: clause}
}

func isNumeric(t FactType) bool { return t == TypeInt || t == TypeDouble }

func isScalar(t FactType) bool {
	return t == TypeBool || t == TypeInt || t == TypeDouble || t == TypeString
}

// checker type-checks one condition against a declared environment, collecting
// the fact and function names it references.
type checker struct {
	env  *Environment
	refs map[string]bool
}

// checkCondition type-checks node and returns its type and the names it
// references.
func checkCondition(n *node, env *Environment) (FactType, map[string]bool, error) {
	c := &checker{env: env, refs: map[string]bool{}}
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
		return c.reference(n)
	case nodeCall:
		return c.call(n)
	case nodeUnary:
		return c.unary(n)
	case nodeTernary:
		return c.ternary(n)
	case nodeBinary:
		return c.binary(n)
	}
	return "", typeErr("OAR-EXPR-1", n.offset, "unreachable node kind")
}

// list types a list literal: non-empty, every member a string ([OAR-EXPR-7]).
func (c *checker) list(n *node) (FactType, error) {
	if len(n.items) == 0 {
		return "", typeErr("OAR-EXPR-7", n.offset, "the empty list literal [] has no inferable type")
	}
	for _, item := range n.items {
		t, err := c.walk(item)
		if err != nil {
			return "", err
		}
		if t != TypeString {
			return "", typeErr("OAR-EXPR-7", item.offset, "a list literal member is %s, want string", t)
		}
	}
	return TypeListString, nil
}

// reference types a name: the declared fact it names, or else a member, typed
// string, of the map<string, dyn> fact its other names give ([OAR-EXPR-2],
// [OAR-EXPR-13]).
func (c *checker) reference(n *node) (FactType, error) {
	name := n.strValue
	if fact, declared := c.env.Facts[name]; declared {
		c.refs[name] = true
		return fact.Type, nil
	}
	if _, isFn := c.env.Functions[name]; isFn {
		return "", typeErr("OAR-EXPR-16", n.offset, "%s is an observation function and must be called", name)
	}
	if at := strings.LastIndexByte(name, '.'); at > 0 {
		target := name[:at]
		decl, declared := c.env.Facts[target]
		if declared && decl.Type == TypeMap {
			c.refs[target] = true
			return TypeString, nil
		}
		if declared {
			return "", typeErr("OAR-EXPR-2", n.offset, "%s selects a member of %s, which is %s, not a map<string, dyn>", name, target, decl.Type)
		}
	}
	return "", typeErr("OAR-EXPR-2", n.offset, "unknown identifier %s", name)
}

func (c *checker) call(n *node) (FactType, error) {
	name := n.strValue
	args := make([]FactType, 0, len(n.items))
	for _, item := range n.items {
		t, err := c.walk(item)
		if err != nil {
			return "", err
		}
		args = append(args, t)
	}
	switch {
	case name == "size":
		if len(args) != 1 || (args[0] != TypeString && args[0] != TypeListString && args[0] != TypeListMap && args[0] != TypeMap) {
			return "", typeErr("OAR-EXPR-16", n.offset, "size takes one string, list, or map, given (%s)", typeList(args))
		}
		return TypeInt, nil
	case isStringBuiltin(name):
		if len(args) != 2 || args[0] != TypeString || args[1] != TypeString {
			return "", typeErr("OAR-EXPR-16", n.offset, "%s takes (string, string), given (%s)", name, typeList(args))
		}
		return TypeBool, nil
	}
	fn, declared := c.env.Functions[name]
	if !declared {
		if _, isFact := c.env.Facts[name]; isFact {
			return "", typeErr("OAR-EXPR-16", n.offset, "%s is a fact, not an observation function", name)
		}
		return "", typeErr("OAR-EXPR-16", n.offset, "%s is neither a built-in nor a declared observation function", name)
	}
	if len(args) != 1 || args[0] != fn.Sig.Arg {
		return "", typeErr("OAR-EXPR-16", n.offset, "%s takes (%s), given (%s)", name, fn.Sig.Arg, typeList(args))
	}
	// [OAR-FIRE-6] A counter reader names its rule by a string literal, so every
	// read resolves at load.
	if _, isReader := counterReaders[name]; isReader && n.items[0].kind != nodeString {
		return "", typeErr("OAR-FIRE-6", n.offset, "%s takes a string literal naming a rule", name)
	}
	c.refs[name] = true
	return fn.Sig.Ret, nil
}

func (c *checker) unary(n *node) (FactType, error) {
	t, err := c.walk(n.left)
	if err != nil {
		return "", err
	}
	if n.op == "!" {
		if t != TypeBool {
			return "", typeErr("OAR-EXPR-8", n.offset, "! takes bool, given %s", t)
		}
		return TypeBool, nil
	}
	if !isNumeric(t) {
		return "", typeErr("OAR-EXPR-9", n.offset, "unary - takes int or double, given %s", t)
	}
	return t, nil
}

func (c *checker) ternary(n *node) (FactType, error) {
	cond, err := c.walk(n.left)
	if err != nil {
		return "", err
	}
	if cond != TypeBool {
		return "", typeErr("OAR-EXPR-8", n.offset, "the ternary condition is %s, want bool", cond)
	}
	a, err := c.walk(n.right)
	if err != nil {
		return "", err
	}
	b, err := c.walk(n.third)
	if err != nil {
		return "", err
	}
	if a != b {
		return "", typeErr("OAR-EXPR-8", n.offset, "the ternary branches are %s and %s, which differ", a, b)
	}
	return a, nil
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
	switch op := n.op; op {
	case "&&", "||":
		if left != TypeBool || right != TypeBool {
			return "", typeErr("OAR-EXPR-8", n.offset, "%s takes bool operands, given %s and %s", op, left, right)
		}
		return TypeBool, nil
	case "in":
		if left != TypeString || (right != TypeListString && right != TypeMap) {
			return "", typeErr("OAR-EXPR-12", n.offset, "in takes a string and a list<string> or map<string, dyn>, given %s and %s", left, right)
		}
		return TypeBool, nil
	case "==", "!=":
		if left != right {
			return "", typeErr("OAR-EXPR-11", n.offset, "%s compares %s with %s", op, left, right)
		}
		if !isScalar(left) {
			return "", typeErr("OAR-EXPR-11", n.offset, "%s compares aggregate %s values; test them with size() or in", op, left)
		}
		return TypeBool, nil
	case "<", "<=", ">", ">=":
		if left != right || (!isNumeric(left) && left != TypeString) {
			return "", typeErr("OAR-EXPR-10", n.offset, "%s compares %s with %s; it takes two int, two double, or two string", op, left, right)
		}
		return TypeBool, nil
	case "%":
		if left != TypeInt || right != TypeInt {
			return "", typeErr("OAR-EXPR-9", n.offset, "%% takes two int, given %s and %s", left, right)
		}
		return TypeInt, nil
	default:
		if op == "+" && left == TypeString && right == TypeString {
			return TypeString, nil
		}
		if left != right || !isNumeric(left) {
			return "", typeErr("OAR-EXPR-9", n.offset, "%s takes two int or two double, given %s and %s", op, left, right)
		}
		return left, nil
	}
}

func typeList(types []FactType) string {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = string(t)
	}
	return strings.Join(names, ", ")
}
