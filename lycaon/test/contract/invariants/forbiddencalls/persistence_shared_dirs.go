package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// designatedSharedDirPruners are the only functions permitted to remove a
// directory under the shared blobstore/persistence root.
var designatedSharedDirPruners = map[string]bool{
	"internal/blobstore/blobstore.go:removeBlob":             true,
	"internal/blobstore/blobstore.go:pruneExpiredLocked":     true,
	"internal/blobstore/blobstore.go:pruneStagingLocked":     true,
	"internal/blobstore/blobstore.go:pruneRetentions":        true,
	"internal/blobstore/blobstore.go:releaseRetentionMarker": true,
	"internal/bloblifecycle/remove.go:RemoveTree":            true,
}

var persistenceSharedDirs = []invariants.Invariant{{
	// No code under the data root removes a shared directory outside the
	// designated pruners (GB10 I7, first case GB03-F1 / #2924).
	Name: "persistence-no-shared-dir-removal-outside-pruners",
	Anchors: []invariants.Anchor{
		invariants.Func("internal/blobstore", "Store.Put"),
		invariants.Func("internal/blobstore", "Store.Maintain"),
	},
	Check: func(r *invariants.Run) {
		files := r.GoFiles(invariants.Production,
			"internal/blobstore",
			"internal/contentblob",
			"internal/blobcache",
			"internal/bloblifecycle",
		)
		for _, f := range files {
			f.Funcs(func(fn *ast.FuncDecl) {
				fnKey := f.Rel + ":" + fn.Name.Name
				f.Calls(fn.Body, func(call *ast.CallExpr, pkg, name string) {
					if pkg != "os" || (name != "Remove" && name != "RemoveAll") || len(call.Args) == 0 {
						return
					}
					if name == "Remove" && !isSharedDirArg(call.Args[0]) {
						return
					}
					if designatedSharedDirPruners[fnKey] || r.Exempt(fnKey) {
						return
					}
					r.ReportAt(call.Pos(), "os.%s removes a shared directory in %s outside designated pruners (GB10 I7)", name, fn.Name.Name)
				})
			})
		}
	},
}}

// isSharedDirArg detects os.Remove(filepath.Dir(...)) or os.Remove(stagingDir)
// patterns that delete shared parent directories on write/release paths.
func isSharedDirArg(expr ast.Expr) bool {
	switch e := ast.Unparen(expr).(type) {
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Dir" {
			return true
		}
	case *ast.Ident:
		return e.Name == "stagingDir" || e.Name == "stageDir" || e.Name == "dir"
	}
	return false
}
