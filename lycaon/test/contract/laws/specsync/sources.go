package specsync

import (
	"go/ast"
	"path"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/laws"
)

const denTypesPath = "lycaon-den/src/api/types.ts"

var (
	denTypes  = laws.File(denTypesPath)
	apiPkg    = laws.Package("pkg/api")
	routeDoor = laws.Decl("internal/api", "Server.setupRoutes")
)

// apiFiles are the production files of lycaon/pkg/api.
func apiFiles(r *laws.Run) []*ast.File {
	var files []*ast.File
	for _, f := range r.GoFiles(laws.Production, "pkg/api") {
		files = append(files, f.AST)
	}
	return files
}

// serverRoutes are the routes internal/api registers, generated operations
// resolved.
func serverRoutes(r *laws.Run) []wirespec.Route {
	return laws.Part(r.Corpus, "server routes", func() ([]wirespec.Route, error) {
		var files []*ast.File
		for _, f := range r.GoFiles(laws.Production, "internal/api") {
			if f.Dir == "internal/api" && wirespec.IsRouteFile(path.Base(f.Rel)) {
				files = append(files, f.AST)
			}
		}
		return wirespec.ServerRoutes(files, operationsByVariable(r)), nil
	})
}

// specRoutes are the operations docs/openapi.yaml documents.
func specRoutes(r *laws.Run) []wirespec.Route {
	return laws.Part(r.Corpus, "openapi routes", func() ([]wirespec.Route, error) {
		return spec(r).Routes()
	})
}

// readRepo reads a repository file for loaders that take a reader.
func readRepo(r *laws.Run) wirespec.ReadFile {
	return func(rel string) ([]byte, error) { return r.Bytes(strings.TrimPrefix(rel, "./")), nil }
}

// operationsByVariable maps each generated operation variable to its route.
func operationsByVariable(r *laws.Run) map[string]wirespec.Route {
	return wirespec.Operations(r.GoFile("internal/api/operations.generated.go").AST)
}
