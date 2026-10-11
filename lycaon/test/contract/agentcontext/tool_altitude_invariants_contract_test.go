package contract

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestAltitudeInvariantOneRuntimeSubstrate(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internalDir := filepath.Join(root, "lycaon", "internal")

	recordPaths := map[string]bool{}
	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		if strings.Contains(path, string(filepath.Separator)+"logoutline"+string(filepath.Separator)) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, body, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				if typ, ok := spec.(*ast.TypeSpec); ok && runtimeEvidenceRecord(typ) {
					recordPaths[path] = true
				}
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal", err)

	want := filepath.Join(internalDir, "evidence", "record.go")
	if len(recordPaths) != 1 || !recordPaths[want] {
		t.Fatalf("runtime evidence Record must live only in evidence/record.go; found %v", sortedKeys(recordPaths))
	}

	resolvePath := filepath.Join(internalDir, "evidence", "resolve.go")
	resolveBody, err := os.ReadFile(resolvePath)
	contractcheck.FailErr(t, "read resolve.go", err)
	if !strings.Contains(string(resolveBody), "func Resolve(") {
		t.Fatal("evidence.Resolve missing from resolve.go")
	}

	dbPath := filepath.Join(internalDir, "db", "models.go")
	body, err := os.ReadFile(dbPath)
	contractcheck.FailErr(t, "read db models", err)
	if !strings.Contains(string(body), "type EvidenceRecords struct") {
		t.Fatal("db.EvidenceRecords persistence adapter missing")
	}
}

func TestAltitudeInvariantReadEscalation(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeLargeGoFile(t, tmp, "big.go")
	esc := surveytools.NewReadEscalationStore()
	tool := &surveytools.ReadTool{Boundary: altitudeBoundary(t), Escalation: esc}
	ctx := altitudeCtx(tmp, "esc-read")

	out1, err := tool.Run(context.Background(), map[string]any{"path": "big.go"}, ctx)
	contractcheck.FailErr(t, "first read", err)
	if !strings.Contains(out1, `"mode":"outline"`) && !strings.Contains(out1, `"mode": "outline"`) {
		t.Fatalf("first unbounded read want outline: %s", out1)
	}

	out2, err := tool.Run(context.Background(), map[string]any{"path": "big.go"}, ctx)
	contractcheck.FailErr(t, "second read", err)
	if !strings.Contains(out2, `"mode":"content"`) && !strings.Contains(out2, `"mode": "content"`) {
		t.Fatalf("second unbounded read want literal full: %s", out2)
	}
	if evidence.ReadEvidenceSurvey(map[string]any{"path": "big.go"}, canonicalSurveyJSON(t, out2)) {
		t.Fatal("escalated full read must not be survey-grade")
	}

	other := altitudeCtx(tmp, "esc-other")
	if esc.Strike("esc-other", "big.go") != 0 {
		t.Fatalf("escalation counter must be session-scoped")
	}
	_, err = tool.Run(context.Background(), map[string]any{"path": "big.go", "offset": 1, "limit": 1}, other)
	contractcheck.FailErr(t, "scoped read", err)
	if esc.Strike("esc-other", "big.go") != 0 {
		t.Fatal("expressed-scope read must not bump escalation counter")
	}
}

func TestAltitudeInvariantSurveyTieringAndGloss(t *testing.T) {
	boundary := altitudeBoundary(t)
	ctx := context.Background()

	t.Run("zoomed_survey", func(t *testing.T) {
		tmp := t.TempDir()
		writeGrepHits(t, tmp, safecmd.GrepMaxMatches+50)
		out, err := (&surveytools.GrepTool{Boundary: boundary}).Run(ctx, map[string]any{"pattern": "needle"}, altitudeCtx(tmp, "survey"))
		contractcheck.FailErr(t, "grep overflow", err)
		if !evidence.GrepEvidenceSurvey(map[string]any{"pattern": "needle"}, canonicalSurveyJSON(t, out)) {
			t.Fatal("overflow grep must be survey-grade")
		}
		assertSurveyResponseHasCoverage(t, out)
	})

	t.Run("literal_non_survey", func(t *testing.T) {
		tmp := t.TempDir()
		contractcheck.FailErr(t, "write", os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("alpha\n"), 0o644))
		args := map[string]any{"pattern": "alpha"}
		out, err := (&surveytools.GrepTool{Boundary: boundary}).Run(ctx, args, altitudeCtx(tmp, "literal"))
		contractcheck.FailErr(t, "grep narrow", err)
		if evidence.GrepEvidenceSurvey(args, canonicalSurveyJSON(t, out)) {
			t.Fatal("narrow grep must not be survey-grade at capture")
		}
	})

	t.Run("gloss_fenced", func(t *testing.T) {
		guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
		snapshot := snapshotFromReadLine(t, "a.go", "1| x", 1)
		var gloss []string
		for i := 0; i < 12; i++ {
			gloss = append(gloss, fmt.Sprintf(`{"label":"hint-%d"}`, i))
		}
		provider := &stubCuratorProvider{
			id: "lite",
			responses: []string{fmt.Sprintf(
				`{"selections":[{"path":"a.go","line":1,"excerpt":"x"}],"gloss":[%s]}`,
				strings.Join(gloss, ","),
			)},
		}
		cur, err := llm.NewTestRegistrySummarizer(provider)
		contractcheck.FailErr(t, "NewTestRegistrySummarizer", err)
		got, err := cur.Curate(context.Background(), snapshot, llm.CurationFocus{Target: "focus"}, 3)
		contractcheck.FailErr(t, "Curate", err)
		if len(got.Gloss) > 5 {
			t.Fatalf("gloss lines = %d want <=5", len(got.Gloss))
		}
		for _, g := range got.Gloss {
			if strings.Contains(g.Label, "func ") {
				t.Fatalf("gloss must not carry verbatim code claims: %q", g.Label)
			}
		}
	})

	t.Run("tier1_deterministic_only", func(t *testing.T) {
		tmp := t.TempDir()
		writeLargeGoFile(t, tmp, "big.go")
		out, err := (&surveytools.ReadTool{
			Boundary: boundary, Escalation: surveytools.NewReadEscalationStore(),
		}).Run(ctx, map[string]any{"path": "big.go"}, altitudeCtx(tmp, "tier1-read"))
		contractcheck.FailErr(t, "read Tier 1", err)
		if strings.Contains(out, `"highlights"`) {
			t.Fatal("Tier 1 overflow must not surface curated highlights")
		}
		assertSurveyResponseHasCoverage(t, out)
	})
}

func TestAltitudeRepoMapToolGone(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range []string{
		"lycaon/config/packs/painted-wolf/platform/tools/native-tools.yaml",
		"lycaon-den/src/api/types.ts",
	} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		if strings.Contains(string(body), "repo_map") {
			t.Fatalf("%s must not reference repo_map tool", rel)
		}
	}
	schemaDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	entries, err := os.ReadDir(schemaDir)
	contractcheck.FailErr(t, "read tools/schemas", err)
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		if strings.TrimSuffix(ent.Name(), ".yaml") == "repo_map" {
			t.Fatal("tools/schemas must not include repo_map.yaml")
		}
		body, err := os.ReadFile(filepath.Join(schemaDir, ent.Name()))
		contractcheck.FailErr(t, "read schema "+ent.Name(), err)
		if strings.Contains(string(body), "repo_map") {
			t.Fatalf("tools/schemas/%s must not reference repo_map", ent.Name())
		}
	}
	if tools := loadNativeToolsManifestFlat(t); altitudeContainsString(tools, "repo_map") {
		t.Fatal("native-tools manifest must not list repo_map")
	}
}

func TestAltitudeCompactorBoundary(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "llm", "compaction", "compactor.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse compactor.go", err)

	hasCompact, hasSummarize, hasCurate := false, false, false
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name == nil {
			return true
		}
		switch fn.Name.Name {
		case "Compact":
			hasCompact = true
		case "Summarize":
			hasSummarize = true
		case "Curate":
			hasCurate = true
		}
		return true
	})
	if !hasCompact || !hasSummarize {
		t.Fatal("compactor must retain Compact and Summarize entrypoints")
	}
	if hasCurate {
		t.Fatal("compactor must not call Curate — altitude uses a separate curator contract")
	}
}

func TestAltitudeToolArgSchemas(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)

	locked := map[string][]string{
		"read":     {"kind", "limit", "mode", "offset", "path", "ranges", "symbol"},
		"list_dir": {"cursor", "include_hidden", "max_depth", "max_entries", "offset", "path"},
		"grep":     {"case_insensitive", "context_lines", "include_hidden", "include_ignored", "lang", "max_matches", "offset", "path", "path_glob", "pattern", "structural"},
		"find":     {"include_ignored", "max_depth", "max_results", "name_glob", "offset", "path", "type"},
	}
	for tool, wantKeys := range locked {
		def, ok := schemas.Tools[tool]
		if !ok {
			t.Fatalf("missing schema for %q", tool)
		}
		got := sortedSchemaPropertyKeys(def.Schema)
		if len(got) != len(wantKeys) {
			t.Fatalf("%s arg properties = %v want %v", tool, got, wantKeys)
		}
		for i := range wantKeys {
			if got[i] != wantKeys[i] {
				t.Fatalf("%s arg properties = %v want %v", tool, got, wantKeys)
			}
		}
	}
}

func TestAltitudeGroundingSubstrateGoldenStillGreen(t *testing.T) {
	t.Parallel()
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "pkg/main.go", "offset": 1, "limit": 2}},
			{Name: "grep", ID: "c2", Args: map[string]any{"path": ".", "pattern": "main"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"pkg/main.go","content":"1| package main\n2| func main() {}\n","offset":1,"end_line":2,"limit":2}`,
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}],"receipt":{"tool":"grep"}}`,
		}},
	})
	wantHandles := []string{"grep#1", "read#1"}
	gotHandles := evidence.HandlesSorted(ev)
	if len(gotHandles) != len(wantHandles) {
		t.Fatalf("handles = %v want %v", gotHandles, wantHandles)
	}
	for i, want := range wantHandles {
		if gotHandles[i] != want {
			t.Fatalf("handles = %v want %v", gotHandles, wantHandles)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func altitudeContainsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// Evidence rows carry observed handles or typed gate outcomes. A manifest
// persistence Record is a separate domain even when it shares the type name.
func runtimeEvidenceRecord(typ *ast.TypeSpec) bool {
	if typ.Name.Name != "Record" {
		return false
	}
	st, ok := typ.Type.(*ast.StructType)
	if !ok {
		return false
	}
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			if name.Name == "Handle" || name.Name == "GateType" {
				return true
			}
		}
	}
	return false
}

func TestRuntimeEvidenceRecordClassification(t *testing.T) {
	for _, tc := range []struct {
		source   string
		evidence bool
	}{
		{"package drafts; type Record struct {SessionID, WorkflowID, ManifestYAML string}", false},
		{"package evidence; type Record struct {Handle, GateType string}", true},
		{"package shadow; type Record struct {Handle string}", true},
		{"package shadow; type Record struct {GateType string}", true},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", tc.source, 0)
		contractcheck.FailErr(t, "parse record domain fixture", err)
		typ := file.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
		if got := runtimeEvidenceRecord(typ); got != tc.evidence {
			t.Fatalf("runtime Record classification=%v, want %v for %s", got, tc.evidence, tc.source)
		}
	}
}
