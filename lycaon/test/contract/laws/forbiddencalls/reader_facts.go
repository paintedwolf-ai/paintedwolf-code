package forbiddencalls

import (
	"go/ast"
	"go/types"

	"github.com/lycaon/lycaon/test/contract/laws"
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

var readerFacts = []laws.Rule{{
	// Template variables are built by prompts.Vars, which rejects occurrence
	// data that would shadow a reader fact. Reading the raw facts anywhere
	// else renders a template without that check.
	Name:    "prompt-vars-through-vars",
	Anchors: []laws.Anchor{laws.Func("internal/prompts", "Vars"), laws.Func("internal/prompts", "buildReaderFacts"), laws.Decl("internal/prompts", "Reader.Facts"), laws.Decl("internal/prompts", "ReaderFacts.Map")},
	Check: func(r *laws.Run) {
		for _, pkg := range r.TypedPackages(laws.Source...) {
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
