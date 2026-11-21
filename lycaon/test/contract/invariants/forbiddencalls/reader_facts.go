package forbiddencalls

import (
	"go/ast"
	"go/types"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

const promptsPkg = "github.com/lycaon/lycaon/internal/prompts"

// readerFactsDoors are the one place each way into a reader's raw facts may
// appear: buildReaderFacts builds and projects them, and Vars merges them
// with occurrence data.
var readerFactsDoors = map[string]string{
	"(" + promptsPkg + ".Reader).Facts":    "internal/prompts/reader_facts_vocab.go:Vars",
	"(" + promptsPkg + ".ReaderFacts).Map": "internal/prompts/reader_facts.go:buildReaderFacts",
	promptsPkg + ".ReaderFacts{}":          "internal/prompts/reader_facts.go:buildReaderFacts",
}

var readerFacts = []invariants.Invariant{{
	// Template variables are built by prompts.Vars, which rejects occurrence
	// data that would shadow a reader fact. Reading the raw facts anywhere
	// else renders a template without that check.
	Name:    "prompt-vars-through-vars",
	Anchors: []invariants.Anchor{invariants.Func("internal/prompts", "Vars"), invariants.Func("internal/prompts", "buildReaderFacts"), invariants.Decl("internal/prompts", "Reader.Facts"), invariants.Decl("internal/prompts", "ReaderFacts.Map")},
	Check: func(r *invariants.Run) {
		for _, pkg := range r.TypedPackages(invariants.Source...) {
			for _, f := range pkg.Files {
				ast.Inspect(f.AST, func(n ast.Node) bool {
					var key string
					switch n := n.(type) {
					case *ast.SelectorExpr:
						if sel := pkg.Info.Selections[n]; sel != nil {
							if fn, ok := sel.Obj().(*types.Func); ok {
								key = fn.FullName()
							}
						}
					case *ast.CompositeLit:
						if named, ok := types.Unalias(pkg.Info.TypeOf(n)).(*types.Named); ok && named.Obj().Pkg() != nil {
							key = named.Obj().Pkg().Path() + "." + named.Obj().Name() + "{}"
						}
					}
					door, guarded := readerFactsDoors[key]
					if !guarded || f.Site(n.Pos()) == door || r.Exempt(f.Site(n.Pos())) {
						return true
					}
					r.ReportAt(n.Pos(), "%s outside %s; build template variables with prompts.Vars", key, door)
					return true
				})
			}
		}
	},
}}
