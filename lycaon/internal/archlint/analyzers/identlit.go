package analyzers

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// IdentLit reports a root identity compared with, switched on, or set to a
// string constant its owner package does not declare. An identity spelled ad
// hoc is how alias lists and silent defaults start.
var IdentLit = &analysis.Analyzer{
	Name: "identlit",
	Doc:  "report root identities spelled as constants their owner package does not declare",
	Run:  runIdentLit,
}

var (
	identityFields fieldSet
	identityOwner  string
)

func init() {
	IdentLit.Flags.Var(&identityFields, "identity-fields", "comma-separated identity fields as importpath.Type.Field")
	IdentLit.Flags.StringVar(&identityOwner, "identity-owner", "", "package whose constants may name an identity")
}

func runIdentLit(pass *analysis.Pass) (any, error) {
	if len(identityFields) == 0 || pass.Pkg.Path() == identityOwner || matchesAny(testSupport, pass.Pkg.Path()) {
		return nil, nil
	}
	check := func(field string, value ast.Expr) {
		if spelled, ok := adHocConstant(pass, value); ok {
			pass.Reportf(value.Pos(), "root identity %s spelled as %q; use a constant %s declares", field, spelled, identityOwner)
		}
	}
	for _, file := range productionFiles(pass) {
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BinaryExpr:
				if n.Op == token.EQL || n.Op == token.NEQ {
					if field := identityOf(pass, n.X); field != "" {
						check(field, n.Y)
					} else if field := identityOf(pass, n.Y); field != "" {
						check(field, n.X)
					}
				}
			case *ast.SwitchStmt:
				if field := identityOf(pass, n.Tag); field != "" {
					for _, stmt := range n.Body.List {
						for _, value := range stmt.(*ast.CaseClause).List {
							check(field, value)
						}
					}
				}
			case *ast.AssignStmt:
				if len(n.Lhs) == len(n.Rhs) {
					for i, lhs := range n.Lhs {
						if field := identityOf(pass, lhs); field != "" {
							check(field, n.Rhs[i])
						}
					}
				}
			case *ast.CompositeLit:
				for _, elt := range n.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if field := literalField(pass, n, kv.Key); field != "" {
							check(field, kv.Value)
						}
					}
				}
			}
			return true
		})
	}
	return nil, nil
}

// identityOf names the identity field a selector reads, if it reads one.
func identityOf(pass *analysis.Pass, expr ast.Expr) string {
	sel, ok := ast.Unparen(expr).(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	selection := pass.TypesInfo.Selections[sel]
	if selection == nil || selection.Kind() != types.FieldVal {
		return ""
	}
	return identityFields.match(selection.Recv(), sel.Sel.Name)
}

// literalField names the identity field a keyed struct literal sets.
func literalField(pass *analysis.Pass, lit *ast.CompositeLit, key ast.Expr) string {
	ident, ok := key.(*ast.Ident)
	if !ok {
		return ""
	}
	t := pass.TypesInfo.TypeOf(lit)
	if t == nil {
		return ""
	}
	return identityFields.match(t, ident.Name)
}

// adHocConstant reports a non-empty string constant that is not a named
// constant of the identity owner.
func adHocConstant(pass *analysis.Pass, expr ast.Expr) (string, bool) {
	tv, ok := pass.TypesInfo.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	value := constant.StringVal(tv.Value)
	if value == "" {
		return "", false
	}
	var name *ast.Ident
	switch e := ast.Unparen(expr).(type) {
	case *ast.Ident:
		name = e
	case *ast.SelectorExpr:
		name = e.Sel
	}
	if name != nil {
		if c, ok := pass.TypesInfo.Uses[name].(*types.Const); ok && c.Pkg() != nil && c.Pkg().Path() == identityOwner {
			return "", false
		}
	}
	return value, true
}

// fieldSet holds identity fields keyed by "importpath.Type" and field name.
type fieldSet map[string]map[string]bool

func (s *fieldSet) String() string { return "" }

func (s *fieldSet) Set(v string) error {
	set := fieldSet{}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		dot := strings.LastIndex(part, ".")
		if dot <= 0 {
			return errBadField(part)
		}
		owner, field := part[:dot], part[dot+1:]
		if !strings.Contains(owner, ".") {
			return errBadField(part)
		}
		if set[owner] == nil {
			set[owner] = map[string]bool{}
		}
		set[owner][field] = true
	}
	*s = set
	return nil
}

func (s fieldSet) match(t types.Type, field string) string {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return ""
	}
	owner := named.Obj().Pkg().Path() + "." + named.Obj().Name()
	if s[owner][field] {
		return owner + "." + field
	}
	return ""
}

type errBadField string

func (e errBadField) Error() string {
	return "identity field " + string(e) + " is not importpath.Type.Field"
}
