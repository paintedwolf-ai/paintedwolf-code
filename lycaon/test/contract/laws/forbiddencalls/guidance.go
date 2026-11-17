package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var guidanceEmission = []laws.Rule{{
	// Agent feedback is emitted through registered hint codes and templates,
	// never as inline prose.
	Name: "guidance-no-inline-emission",
	Anchors: []laws.Anchor{
		laws.Func("internal/coordinator/kick", "KickEngine.QueuePendingText"),
		laws.Func("internal/guidance", "FormatCoordinatorNudge"),
	},
	Check: func(r *laws.Run) {
		scan := r.GuidanceEmission()
		for _, site := range scan.QueuePendingTextSites {
			r.Report("%s: QueuePendingText; queue a registered hint through the coordinator nudge", site)
		}
		for _, site := range scan.InlineCodeNudgeLiterals {
			r.Report("%s: inline \"Code: …\" nudge literal; register a hint and template", site)
		}
	},
}, {
	// Tool envelope packages carry hints by registered code.
	Name:    "tool-envelope-no-inline-hint-consts",
	Anchors: []laws.Anchor{laws.Func("internal/guidance", "EnvelopeHintMessage")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/scan", "internal/repomap") {
			for _, decl := range f.AST.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					value := spec.(*ast.ValueSpec)
					for i, name := range value.Names {
						if !strings.HasSuffix(name.Name, "Hint") || i >= len(value.Values) {
							continue
						}
						if _, ok := laws.StringLit(value.Values[i]); ok {
							r.ReportAt(name.Pos(), "const %s holds hint prose; use the hint registry", name.Name)
						}
					}
				}
			}
		}
	},
}}
