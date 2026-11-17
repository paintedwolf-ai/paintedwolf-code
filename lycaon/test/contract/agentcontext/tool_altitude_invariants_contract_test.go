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
	schemas, err := toolschema.LoadSchemaDirWithOperations(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
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
