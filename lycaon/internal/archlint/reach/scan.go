package reach

import (
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// tableRead matches a table named where SQL reads it. DELETE FROM is a write.
var tableRead = regexp.MustCompile(`(?i)(\bdelete\s+)?\b(?:from|join)\s+["` + "`" + `]?([a-z_][a-z0-9_]*)`)

func environ() []string { return os.Environ() }

func relPos(dir string, pos token.Position) string {
	if rel, err := filepath.Rel(dir, pos.Filename); err == nil && !strings.HasPrefix(rel, "..") {
		pos.Filename = rel
	}
	return pos.String()
}

// scanFunction records what one reachable function reads and which context
// keys it sets or reads.
func scanFunction(fset *token.FileSet, fn *ssa.Function, cfg Config, p *pass) {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			for _, op := range instr.Operands(nil) {
				if c, ok := (*op).(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
					for _, m := range tableRead.FindAllStringSubmatch(constant.StringVal(c.Value), -1) {
						if m[1] == "" {
							p.reads[strings.ToLower(m[2])] = true
						}
					}
				}
			}
			call, ok := instr.(*ssa.Call)
			if !ok {
				continue
			}
			common := call.Common()
			switch {
			case common.StaticCallee() != nil && common.StaticCallee().String() == "context.WithValue" && len(common.Args) == 3:
				if id, ok := keyID(common.Args[1], cfg); ok {
					p.keysSet[id] = true
				}
			case common.IsInvoke() && common.Method.FullName() == "(context.Context).Value" && len(common.Args) == 1:
				if id, ok := keyID(common.Args[0], cfg); ok {
					if _, seen := p.keysRead[id]; !seen {
						p.keysRead[id] = Finding{
							Kind: KindUnsetCtxKey, Key: id, Pos: relPos(cfg.Dir, fset.Position(call.Pos())),
							Message: "context key " + id + " is read but no shipped program sets it",
						}
					}
				}
			}
		}
	}
}

// keyID names a context key by its constant value, global or named type, and
// reports whether the key belongs to a reported package.
func keyID(v ssa.Value, cfg Config) (string, bool) {
	if mi, ok := v.(*ssa.MakeInterface); ok {
		v = mi.X
	}
	if load, ok := v.(*ssa.UnOp); ok && load.Op == token.MUL {
		if g, ok := load.X.(*ssa.Global); ok && g.Pkg != nil {
			return g.Pkg.Pkg.Path() + "." + g.Name(), cfg.reported(g.Pkg.Pkg.Path())
		}
	}
	t := v.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", false
	}
	id := named.Obj().Pkg().Path() + "." + named.Obj().Name()
	if c, ok := v.(*ssa.Const); ok && c.Value != nil {
		id += "(" + c.Value.ExactString() + ")"
	}
	return id, cfg.reported(named.Obj().Pkg().Path())
}

// unsetKeys reports keys some GOOS reads that no GOOS sets.
func unsetKeys(passes []*pass) []Finding {
	set := map[string]bool{}
	read := map[string]Finding{}
	for _, p := range passes {
		for id := range p.keysSet {
			set[id] = true
		}
		for id, f := range p.keysRead {
			if _, seen := read[id]; !seen {
				read[id] = f
			}
		}
	}
	var out []Finding
	for id, f := range read {
		if !set[id] {
			out = append(out, f)
		}
	}
	return out
}

// writeOnlyTables reports tables no reachable query or schema view reads.
func writeOnlyTables(schema Schema, passes []*pass) []Finding {
	read := map[string]bool{}
	for _, p := range passes {
		for t := range p.reads {
			read[t] = true
		}
	}
	for _, body := range schema.Readers {
		for _, m := range tableRead.FindAllStringSubmatch(body, -1) {
			if m[1] == "" {
				read[strings.ToLower(m[2])] = true
			}
		}
	}
	var out []Finding
	for _, t := range schema.Tables {
		if !read[strings.ToLower(t.Name)] {
			out = append(out, Finding{
				Kind: KindWriteOnlyTable, Key: t.Name, Pos: t.Pos,
				Message: "table " + t.Name + " has no reader a shipped program reaches",
			})
		}
	}
	return out
}
