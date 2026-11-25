package rejectinventory

import (
	"fmt"
	"go/ast"
	"go/types"
	"slices"
)

type fieldWrite struct {
	decl  funcDecl
	value ast.Expr
}

func (s *scan) writesOf(tn *types.TypeName, field string) []fieldWrite {
	key := typeKey(tn) + "." + field
	writes, ok := s.fieldWrites[key]
	if !ok {
		writes = s.collectFieldWrites(tn, field)
		s.fieldWrites[key] = writes
	}
	return writes
}

// resolveFieldProducers resolves a non-carrier field through every keyed
// literal of its type and every assignment to it. A type declared outside the
// scanned packages has producers the inventory cannot see.
func (s *scan) resolveFieldProducers(tn *types.TypeName, field string) resolution {
	key := typeKey(tn) + "." + field
	if slices.Contains(s.relays, key) {
		return resolution{outcome: OutcomeForwardedCarrier, upstream: key}
	}
	if !s.scansPackage(tn.Pkg()) {
		return unresolved("field " + key + " is declared outside the scanned packages")
	}
	return s.node("field "+key, func() resolution {
		writes := s.writesOf(tn, field)
		enumerate, fromData := s.decoded[key]
		if len(writes) == 0 && !fromData {
			return unresolved("field " + key + " has no module producer")
		}
		var out resolution
		if fromData {
			// Module code copies this field from serialized data, so the data's
			// production loader is the producer of the values it cannot follow.
			codes, err := enumerate()
			if err != nil {
				return unresolved("data producer " + key + ": " + err.Error())
			}
			out = resolution{codes: codes, upstream: key}
		}
		for _, w := range writes {
			r := s.resolve(w.decl, w.value, 0)
			if r.outcome == OutcomeUnresolved {
				if fromData {
					continue
				}
				rel, line := position(w.decl.pkg, s.root, w.value.Pos())
				r.reason = fmt.Sprintf("%s (writer %s at %s:%d)", r.reason, key, rel, line)
			}
			out.merge(r)
		}
		return out
	})
}

func (s *scan) scansPackage(pkg *types.Package) bool {
	if pkg == nil {
		return false
	}
	for _, p := range s.pkgs {
		if p.PkgPath == pkg.Path() {
			return true
		}
	}
	return false
}

func (s *scan) collectFieldWrites(tn *types.TypeName, field string) []fieldWrite {
	want := typeKey(tn)
	var writes []fieldWrite
	for _, d := range s.decls {
		info := d.pkg.TypesInfo
		ast.Inspect(d.decl.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				tv, ok := info.Types[node]
				if !ok || typeKey(namedOf(tv.Type)) != want {
					return true
				}
				for _, elt := range node.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == field {
							writes = append(writes, fieldWrite{decl: d, value: kv.Value})
						}
					}
				}
			case *ast.AssignStmt:
				if len(node.Lhs) != len(node.Rhs) {
					return true
				}
				for i, lhs := range node.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != field {
						continue
					}
					if selection, ok := info.Selections[sel]; ok && typeKey(declaringType(selection)) == want {
						writes = append(writes, fieldWrite{decl: d, value: node.Rhs[i]})
					}
				}
			}
			return true
		})
	}
	return writes
}

// declaringType returns the struct that declares a selected field, following
// embedded fields.
func declaringType(sel *types.Selection) *types.TypeName {
	t := sel.Recv()
	index := sel.Index()
	for _, i := range index[:len(index)-1] {
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			return nil
		}
		t = st.Field(i).Type()
	}
	return namedOf(t)
}
