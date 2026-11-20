package wirespec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

var pathParamRE = regexp.MustCompile(`\{[^}]+\}`)

// LoadServerRoutes parses the route files of internal/api in a checkout.
func LoadServerRoutes(repoRoot string) ([]Route, error) {
	apiDir := filepath.Join(repoRoot, "lycaon", "internal", "api")
	paths, err := filepath.Glob(filepath.Join(apiDir, "*_routes.go"))
	if err != nil {
		return nil, err
	}
	paths = append([]string{filepath.Join(apiDir, "server.go"), filepath.Join(apiDir, "routes.go")}, paths...)
	fset := token.NewFileSet()
	parse := func(path string) (*ast.File, error) { return parser.ParseFile(fset, path, nil, 0) }
	generated, err := parse(filepath.Join(apiDir, "operations.generated.go"))
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, path := range paths {
		f, err := parse(path)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return ServerRoutes(files, Operations(generated)), nil
}

// IsRouteFile reports whether a file of internal/api registers routes.
func IsRouteFile(name string) bool {
	return name == "server.go" || name == "routes.go" || strings.HasSuffix(name, "_routes.go")
}

// ServerRoutes returns the routes the route files register, resolving
// generated operations through operations.
func ServerRoutes(files []*ast.File, operations map[string]Route) []Route {
	var routes []Route
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			prefix := ""
			if fn.Name.Name != "setupRoutes" {
				prefix = "/v1"
			}
			collectServerRoutes(fn.Body, prefix, operations, &routes)
		}
	}
	return routes
}

func collectServerRoutes(body *ast.BlockStmt, prefix string, operations map[string]Route, routes *[]Route) {
	for _, stmt := range body.List {
		exprStmt, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := exprStmt.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		name := ""
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		if name == "registerV1Operation" || name == "registerRootOperation" {
			if len(call.Args) < 2 {
				continue
			}
			operation, ok := call.Args[1].(*ast.Ident)
			if !ok {
				continue
			}
			if route, ok := operations[operation.Name]; ok {
				*routes = append(*routes, route)
			}
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		method := strings.ToUpper(sel.Sel.Name)
		switch method {
		case "GET", "POST", "PUT", "DELETE", "PATCH":
			if len(call.Args) < 1 {
				continue
			}
			pathLit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || pathLit.Kind != token.STRING {
				continue
			}
			segment := strings.Trim(pathLit.Value, `"`)
			full := joinRoutePath(prefix, segment)
			*routes = append(*routes, Route{
				Method: method,
				Path:   full,
			})
		case "ROUTE":
			if len(call.Args) < 2 {
				continue
			}
			pathLit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || pathLit.Kind != token.STRING {
				continue
			}
			fnLit, ok := call.Args[1].(*ast.FuncLit)
			if !ok || fnLit.Body == nil {
				continue
			}
			segment := strings.Trim(pathLit.Value, `"`)
			collectServerRoutes(fnLit.Body, joinRoutePath(prefix, segment), operations, routes)
		}
	}
}

// Operations maps each generated operation variable in
// internal/api/operations.generated.go to its route and operationId.
func Operations(file *ast.File) map[string]Route {
	operations := map[string]Route{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			literal, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			route := Route{}
			for _, element := range literal.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				name, ok := field.Key.(*ast.Ident)
				text, textOK := field.Value.(*ast.BasicLit)
				if !ok || !textOK || text.Kind != token.STRING {
					continue
				}
				value := strings.Trim(text.Value, `"`)
				switch name.Name {
				case "ID":
					route.OperationID = value
				case "Method":
					route.Method = value
				case "Path":
					route.Path = value
				}
			}
			if route.Method != "" && route.Path != "" {
				operations[value.Names[0].Name] = route
			}
		}
	}
	return operations
}

func joinRoutePath(prefix, segment string) string {
	switch {
	case prefix == "":
		return segment
	case segment == "":
		return prefix
	case strings.HasPrefix(segment, "/"):
		return prefix + segment
	default:
		return prefix + "/" + segment
	}
}

func normalizeRoutePath(path string) string {
	return pathParamRE.ReplaceAllString(path, "{}")
}

func RouteKey(method, path string) string {
	return method + " " + normalizeRoutePath(path)
}

func RouteSet(routes []Route) map[string]Route {
	out := make(map[string]Route, len(routes))
	for _, r := range routes {
		out[RouteKey(r.Method, r.Path)] = r
	}
	return out
}
