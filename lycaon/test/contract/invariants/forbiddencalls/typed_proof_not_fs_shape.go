package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var typedProofNotFSShape = []invariants.Invariant{{
	// Completion, artifact, and gate checks never treat directory contents or
	// emptiness (os.ReadDir, filepath.Walk, filepath.WalkDir, io/fs.WalkDir) as
	// proof of work (GB10 I8, first case GB06-F2 / #2911).
	Name: "completion-and-gates-never-read-dir-as-proof",
	Anchors: []invariants.Anchor{
		invariants.Package("internal/session/workercompletion"),
		invariants.Package("internal/session/closeoutassembly"),
		invariants.Package("internal/workflow/gates"),
	},
	Check: func(r *invariants.Run) {
		files := r.GoFiles(invariants.Production,
			"internal/session/workercompletion",
			"internal/session/closeoutassembly",
			"internal/workflow/gates",
		)
		for _, f := range files {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				forbidden := (pkg == "os" && name == "ReadDir") ||
					(pkg == "path/filepath" && (name == "Walk" || name == "WalkDir")) ||
					(pkg == "io/fs" && name == "WalkDir") ||
					name == "Readdir" || name == "Readdirnames"
				if !forbidden {
					return
				}
				if r.Exempt(f.Rel) {
					return
				}
				r.ReportAt(call.Pos(), "%s.%s inspects filesystem directory shape; completion and gate proof must come from typed facts (GB10 I8)", pkg, name)
			})
		}
	},
}}
