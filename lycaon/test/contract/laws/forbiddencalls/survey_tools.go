package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// envelopeOnlyNativeFiles hold the envelope-only analysis tools, which take
// path resolution, confinement and caps from safecmd.
var envelopeOnlyNativeFiles = []string{
	"internal/tools/native/survey/stat.go",
	"internal/tools/native/survey/wc.go",
	"internal/tools/native/survey/list_dir.go",
	"internal/tools/native/survey/list_dir_altitude.go",
	"internal/tools/native/survey/find.go",
	"internal/tools/native/survey/find_walk.go",
	"internal/tools/native/survey/grep.go",
	"internal/tools/native/survey/grep_scope_guard.go",
	"internal/tools/native/jq/jq.go",
	"internal/tools/native/jq/decode.go",
}

// capNameParts mark a host cap constant safecmd declares.
var capNameParts = []string{"Max", "Timeout", "Bytes", "Depth", "Scan", "Zoom", "Entries", "Paths", "Files", "Matches", "Results", "Pattern", "Context", "Recursive"}

var surveyTools = []laws.Rule{{
	Name:    "envelope-only-tools-use-safecmd",
	Anchors: append(goPaths(envelopeOnlyNativeFiles), laws.Package("internal/tools/safecmd")),
	Exempt:  []laws.Exemption{{Key: "internal/tools/native/survey/grep.go:hostGrepMaxFileBytes", Reason: "a package variable tests lower to exercise the clamp"}},
	Check: func(r *laws.Run) {
		banned := map[string]string{
			"github.com/lycaon/lycaon/internal/tools/projectpaths": "ResolveRead",
			"github.com/lycaon/lycaon/internal/confine":            "DefaultConfinement",
			"github.com/lycaon/lycaon/internal/tools/toolkit":      "PathEscapeReject",
		}
		for _, rel := range envelopeOnlyNativeFiles {
			f := r.GoFile(rel)
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if banned[pkg] == name {
					r.ReportAt(sel.Pos(), "%s; take it from safecmd", laws.SelectorName(sel))
				}
			})
			for _, decl := range f.AST.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
					continue
				}
				for _, spec := range gen.Specs {
					for _, name := range spec.(*ast.ValueSpec).Names {
						if !isHostCapName(name.Name) || (gen.Tok == token.VAR && r.Exempt(f.Rel+":"+name.Name)) {
							continue
						}
						r.ReportAt(name.Pos(), "redeclares %s; use safecmd", name.Name)
					}
				}
			}
		}
	},
}, {
	// Survey surfaces walk project trees only through sandbox.SurveyWalk,
	// the one place the prune policy applies.
	Name:    "survey-walks-use-the-shared-primitive",
	Anchors: []laws.Anchor{laws.Func("internal/sandbox", "SurveyWalk")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/tools/native", "internal/repomap", "internal/workspace", "internal/sandbox") {
			if f.Rel == "internal/sandbox/survey_walk.go" {
				continue
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if name == "WalkDir" && (pkg == "path/filepath" || pkg == "io/fs") {
					r.ReportAt(sel.Pos(), "raw %s; walk through sandbox.SurveyWalk", laws.SelectorName(sel))
				}
			})
		}
	},
}}

func isHostCapName(name string) bool {
	if !strings.HasPrefix(name, "host") {
		return false
	}
	for _, part := range capNameParts {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

func goPaths(rels []string) []laws.Anchor {
	out := make([]laws.Anchor, 0, len(rels))
	for _, rel := range rels {
		out = append(out, laws.GoPath(rel))
	}
	return out
}

var toolIngest = []laws.Rule{{
	// A tool reads a whole file only after bounding it: a stat of its size
	// or a spill to disk in the same function.
	Name:    "tool-file-ingest-is-size-bounded",
	Anchors: []laws.Anchor{laws.Package("internal/tools")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/tools") {
			f.Funcs(func(fn *ast.FuncDecl) {
				var slurps []*ast.CallExpr
				bounded := false
				f.Calls(fn.Body, func(call *ast.CallExpr, pkg, name string) {
					callee := laws.CalleeName(call)
					switch {
					case pkg == "os" && name == "ReadFile":
						slurps = append(slurps, call)
					case callee == "Size" || strings.HasSuffix(callee, "SpillBytes"):
						bounded = true
					}
				})
				for _, call := range slurps {
					if !bounded {
						r.ReportAt(call.Pos(), "%s reads a whole file without bounding its size", fn.Name.Name)
					}
				}
			})
		}
	},
}}
