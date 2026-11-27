package forbiddencalls

import (
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

const nativeDir = "internal/tools/native"
const commandDir = nativeDir + "/command"

var commandImports = []invariants.Invariant{{
	Name:    "command-imports-only-declared-native-seams",
	Anchors: []invariants.Anchor{invariants.Package(commandDir)},
	Check: func(r *invariants.Run) {
		native := r.Go().Pkg(nativeDir)
		allowed := []string{native + "/toolkit", native + "/terminal", native + "/command"}
		for _, f := range r.GoFiles(func(f *invariants.GoFile) bool { return f.Dir == commandDir }, commandDir) {
			for _, spec := range f.AST.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				r.Must(err)
				if (importPath == native || strings.HasPrefix(importPath, native+"/")) && !slices.Contains(allowed, importPath) {
					r.ReportAt(spec.Pos(), "command imports %s outside its native seams", importPath)
				}
			}
		}
	},
}}
