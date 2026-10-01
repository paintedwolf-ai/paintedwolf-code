package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// declaredAPIErrorCodes returns the generated code set.
func declaredAPIErrorCodes() map[string]string {
	out := make(map[string]string)
	for _, code := range wire.AllApiErrorCodeValues() {
		out[string(code)] = "declared in docs/openapi/vocab/ApiErrorCode.yaml"
	}
	return out
}

// TestAPIErrorCodesClosure matches emitted API errors to the ledger.
func TestAPIErrorCodesClosure(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internalDir := filepath.Join(root, "lycaon", "internal")
	apiDir := filepath.Join(internalDir, "api")

	emitted := scanAPIErrorCodes(t, apiDir)
	for code := range scanAPIHostErrorCodes(t, apiDir) {
		emitted[code] = true
	}
	for code := range scanWorkerExecuteFailureCodes(t, filepath.Join(internalDir, "worker", "failure.go")) {
		emitted[code] = true
	}
	for code := range scanTypedAPIErrorCodes(t, internalDir) {
		emitted[code] = true
	}
	declared := declaredAPIErrorCodes()

	var missing []string
	for code := range emitted {
		if _, ok := declared[code]; !ok {
			missing = append(missing, code)
		}
	}
	sort.Strings(missing)
	var ghosts []string
	for code := range declared {
		if !emitted[code] {
			ghosts = append(ghosts, code)
		}
	}
	sort.Strings(ghosts)

	var violations []string
	for _, code := range missing {
		violations = append(violations, "handler emits undeclared code "+code+" — add it to docs/openapi/vocab/ApiErrorCode.yaml")
	}
	for _, code := range ghosts {
		violations = append(violations, "ledger lists ghost code "+code+" — remove entry or wire emission site")
	}
	contractcheck.FailViolations(t, "API error code ledger closure drift", violations)
}

// apiErrorCodeVocabValue is one ApiErrorCode vocabulary entry as authored.
type apiErrorCodeVocabValue struct {
	ID     string
	Status []int
	Keys   []string
}

func loadAPIErrorCodeVocab(t *testing.T) []apiErrorCodeVocabValue {
	t.Helper()
	path := filepath.Join(contractcheck.RepoRoot(t), "docs", "openapi", "vocab", "ApiErrorCode.yaml")
	raw, err := os.ReadFile(path) // #nosec G304 -- repository fixture
	contractcheck.FailErr(t, "read ApiErrorCode vocabulary", err)
	var doc struct {
		Values []yaml.Node `yaml:"values"`
	}
	contractcheck.FailErr(t, "decode ApiErrorCode vocabulary", yaml.Unmarshal(raw, &doc))
	out := make([]apiErrorCodeVocabValue, 0, len(doc.Values))
	for _, node := range doc.Values {
		var value apiErrorCodeVocabValue
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, val := node.Content[i].Value, node.Content[i+1]
			value.Keys = append(value.Keys, key)
			switch key {
			case "id":
				value.ID = val.Value
			case "status":
				status, err := strconv.Atoi(val.Value)
				contractcheck.FailErr(t, "parse status of "+value.ID, err)
				value.Status = append(value.Status, status)
			}
		}
		out = append(out, value)
	}
	return out
}

// TestAPIErrorCodeStatus proves each code maps to exactly one HTTP error
// status and that the generated lookup agrees with the vocabulary.
func TestAPIErrorCodeStatus(t *testing.T) {
	t.Parallel()
	vocab := loadAPIErrorCodeVocab(t)
	var violations []string
	for _, value := range vocab {
		if len(value.Status) != 1 {
			violations = append(violations, fmt.Sprintf("%s declares %d statuses; exactly one is required", value.ID, len(value.Status)))
			continue
		}
		status := value.Status[0]
		if status < 400 || status > 599 {
			violations = append(violations, fmt.Sprintf("%s declares non-error status %d", value.ID, status))
		}
		if got := wire.ApiErrorCode(value.ID).HTTPStatus(); got != status {
			violations = append(violations, fmt.Sprintf("%s: generated HTTPStatus() = %d, vocabulary says %d", value.ID, got, status))
		}
		if status == 404 && value.ID != "not_found" && !strings.HasSuffix(value.ID, "_not_found") {
			violations = append(violations, value.ID+" answers 404 but is not named <resource>_not_found")
		}
		for _, key := range value.Keys {
			if key == "deprecated" || key == "successor" {
				violations = append(violations, value.ID+" carries "+key+"; retired codes are deleted, not deprecated")
			}
		}
	}
	if len(vocab) != len(wire.AllApiErrorCodeValues()) {
		violations = append(violations, fmt.Sprintf("vocabulary lists %d codes but Go declares %d", len(vocab), len(wire.AllApiErrorCodeValues())))
	}
	contractcheck.FailViolations(t, "API error code status drift", violations)
}

// TestAPIErrorCodeStyle requires lowercase snake_case codes.
func TestAPIErrorCodeStyle(t *testing.T) {
	t.Parallel()
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	for code := range declaredAPIErrorCodes() {
		if !snake.MatchString(code) {
			t.Errorf("error code %q is not lowercase snake_case", code)
		}
	}
}

// scanAPIErrorCodes finds literal codes at handler emission sites.
func scanAPIErrorCodes(t *testing.T, apiDir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	constants := apiErrorCodeConstants(t, filepath.Join(apiDir, "..", "..", "pkg", "api", "api_error_code_ids.generated.go"))

	corpus, err := contractcheck.LoadGoASTCorpusMode(apiDir, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "load API source corpus", err)
	for _, source := range corpus.Production() {
		f := source.AST
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				// View failures are resource state, not HTTP error codes.
				if typ, ok := node.Type.(*ast.SelectorExpr); ok && typ.Sel.Name == "SourceViewFailure" {
					return false
				}
			case *ast.CallExpr:
				// Fail* carry the code second; the positional forms carry it third.
				name, ok := astErrorEmitCallee(node.Fun)
				if !ok {
					return true
				}
				index := -1
				switch name {
				case "Fail", "FailReason", "FailDetails":
					index = 1
				case "Error", "ErrorContext", "ReasonError":
					index = 2
				}
				if index >= 0 && index < len(node.Args) {
					if v := astErrorCode(node.Args[index], constants); v != "" && contractcheck.IsErrorCodeShape(v) {
						out[v] = true
					}
				}
			case *ast.SelectorExpr:
				// A generated constant names a code wherever a handler passes it on.
				if code, ok := constants[node.Sel.Name]; ok {
					out[code] = true
				}
			case *ast.KeyValueExpr:
				// Code: "<code>" inside any composite literal (typically
				// wire.ErrorResponse, ComposeValidationError).
				key, ok := node.Key.(*ast.Ident)
				if !ok || key.Name != "Code" {
					return true
				}
				if v := astErrorCode(node.Value, constants); v != "" && contractcheck.IsErrorCodeShape(v) {
					out[v] = true
				}
			}
			return true
		})
		scanAPIReturnedErrorCodes(f, out, constants)
	}
	return out
}

// scanTypedAPIErrorCodes finds codes other host packages hand to the API:
// generated constant references and constants declared as wire.ApiErrorCode.
func scanTypedAPIErrorCodes(t *testing.T, internalDir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	constants := apiErrorCodeConstants(t, filepath.Join(internalDir, "..", "pkg", "api", "api_error_code_ids.generated.go"))
	corpus, err := contractcheck.LoadGoASTCorpusMode(internalDir, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "load internal source corpus", err)
	for _, source := range corpus.Production() {
		ast.Inspect(source.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if code, ok := constants[node.Sel.Name]; ok {
					out[code] = true
				}
			case *ast.ValueSpec:
				typ, ok := node.Type.(*ast.SelectorExpr)
				if !ok || typ.Sel.Name != "ApiErrorCode" {
					return true
				}
				for _, value := range node.Values {
					if code := contractcheck.AstStringLit(value); code != "" {
						out[code] = true
					}
				}
			}
			return true
		})
	}
	return out
}

func scanAPIReturnedErrorCodes(file *ast.File, out map[string]bool, constants map[string]string) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		indexes := apiErrorCodeResultIndexes(fn.Type.Results)
		if len(indexes) == 0 {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for _, index := range indexes {
				if index >= len(ret.Results) {
					continue
				}
				if code := astErrorCode(ret.Results[index], constants); code != "" && contractcheck.IsErrorCodeShape(code) {
					out[code] = true
				}
			}
			return true
		})
	}
}

func apiErrorCodeResultIndexes(results *ast.FieldList) []int {
	if results == nil {
		return nil
	}
	var indexes []int
	resultIndex := 0
	for _, field := range results.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		selector, ok := field.Type.(*ast.SelectorExpr)
		if ok && selector.Sel != nil && selector.Sel.Name == "ApiErrorCode" {
			for offset := range count {
				indexes = append(indexes, resultIndex+offset)
			}
		}
		resultIndex += count
	}
	return indexes
}

func apiErrorCodeConstants(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "parse generated API error codes", err)
	out := map[string]string{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			if code := contractcheck.AstStringLit(value.Values[0]); code != "" {
				out[value.Names[0].Name] = code
			}
		}
	}
	return out
}

func astErrorCode(expr ast.Expr, constants map[string]string) string {
	if code := contractcheck.AstStringLit(expr); code != "" {
		return code
	}
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil {
		return ""
	}
	return constants[selector.Sel.Name]
}

func astErrorEmitCallee(fun ast.Expr) (string, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name, true
	case *ast.SelectorExpr:
		// slog shares the Error/ErrorContext names; its third argument is a log key.
		if f.Sel != nil && !astLoggerReceiver(f.X) {
			return f.Sel.Name, true
		}
	}
	return "", false
}

// astLoggerReceiver reports slog package calls and Logger fields.
func astLoggerReceiver(x ast.Expr) bool {
	switch r := x.(type) {
	case *ast.Ident:
		return r.Name == "slog"
	case *ast.SelectorExpr:
		return r.Sel != nil && (r.Sel.Name == "Logger" || r.Sel.Name == "logger")
	case *ast.CallExpr:
		return astLoggerReceiver(r.Fun)
	}
	return false
}
