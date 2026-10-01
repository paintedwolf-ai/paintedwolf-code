package contract

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestWorkflowRunHandlersUseEnrichmentSeam: handlers that emit *wire.WorkflowRun
// call s.WriteWorkflowRun or s.EnrichWorkflowRun(s) first. Enrichment attaches
// derived fields, such as manifest_tier, that workflow_runs does not persist.
func TestWorkflowRunHandlersUseEnrichmentSeam(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	apiDir := filepath.Join(root, "lycaon", "internal", "api")

	var violations []string
	err := filepath.WalkDir(apiDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		violations = append(violations, scanFileForUnenrichedWorkflowRunEmits(t, path)...)
		return nil
	})
	testutil.FailErr(t, "walk internal/api", err)

	if len(violations) > 0 {
		contractcheck.FailViolations(t, "HTTP handlers emitting WorkflowRun must enrich via WriteWorkflowRun(s)/EnrichWorkflowRun(s); unenriched runs ship without manifest_tier and UI chrome", violations)
	}
}

// scanFileForUnenrichedWorkflowRunEmits walks each function in the file and
// flags any that emit a WorkflowRun-shaped value via writeJSON without first
// going through the enrichment seam.
func scanFileForUnenrichedWorkflowRunEmits(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	testutil.FailErr(t, "parse "+filepath.Base(path), err)

	var violations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// Matches type references in the rendered body text; no type checking.
		body := renderNode(fset, fn.Body)

		if !strings.Contains(body, "WriteJSON(") {
			continue
		}
		emitsWorkflowRun := mentionsWorkflowRunType(body)
		if !emitsWorkflowRun {
			continue
		}

		// WriteWorkflowRun(s) enriches internally.
		if strings.Contains(body, "WriteWorkflowRun(") || strings.Contains(body, "WriteWorkflowRuns(") {
			continue
		}
		if strings.Contains(body, "EnrichWorkflowRun(") || strings.Contains(body, "EnrichWorkflowRuns(") {
			continue
		}

		pos := fset.Position(fn.Pos())
		violations = append(violations,
			filepath.Base(pos.Filename)+":"+strconv.Itoa(pos.Line)+": "+fn.Name.Name+
				" emits WorkflowRun via writeJSON without enrichment seam — use s.WriteWorkflowRun(...) or call s.EnrichWorkflowRun(s) first")
	}
	return violations
}

// mentionsWorkflowRunType is a text check for WorkflowRun references in a
// function body. A false positive satisfies the check by using the seam.
func mentionsWorkflowRunType(body string) bool {
	markers := []string{
		"wire.WorkflowRun",
		"api.WorkflowRun",
		"ActiveWorkflowRun",
		"WorkflowRun{",
	}
	for _, m := range markers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

func renderNode(fset *token.FileSet, n ast.Node) string {
	var sb strings.Builder
	_ = printer.Fprint(&sb, fset, n)
	return sb.String()
}

// The positive control keeps the unenriched-handler detector live.
func TestWireEnrichmentRuleFiresOnUnenrichedHandler(t *testing.T) {
	src := `package api
import (
	"net/http"
	wire "github.com/lycaon/lycaon/pkg/api"
)
type Server struct{}

func (s *Server) Bad(w http.ResponseWriter, r *http.Request) {
	run := &wire.WorkflowRun{}
	httpio.WriteJSON(w, http.StatusOK, run)
}
func (s *Server) Good(w http.ResponseWriter, r *http.Request) {
	run := &wire.WorkflowRun{}
	s.SessionView.WriteWorkflowRun(w, r, http.StatusOK, run)
}
func (s *Server) GoodExplicit(w http.ResponseWriter, r *http.Request) {
	run := &wire.WorkflowRun{}
	s.SessionView.EnrichWorkflowRun(r.Context(), run)
	httpio.WriteJSON(w, http.StatusOK, run)
}
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "synthetic.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		testutil.FailErr(t, "write synthetic", err)
	}
	got := scanFileForUnenrichedWorkflowRunEmits(t, path)
	var sawBad bool
	var falsePositives []string
	for _, v := range got {
		if strings.Contains(v, "Bad ") {
			sawBad = true
			continue
		}
		falsePositives = append(falsePositives, v)
	}
	if !sawBad {
		t.Fatalf("rule failed to flag synthetic Bad handler; violations = %v", got)
	}
	if len(falsePositives) > 0 {
		t.Fatalf("rule false-positived on enriched handlers: %v", falsePositives)
	}
}
