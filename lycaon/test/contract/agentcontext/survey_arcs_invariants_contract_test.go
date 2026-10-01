package contract

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/survey"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// closeout invariants — drift-proof arc-scale curation contracts.

func TestSurveyArcSurveyRepoNoInlineCurate(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range []string{
		"lycaon/internal/survey/repo_tool.go",
		"lycaon/internal/survey/runner.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		src := string(body)
		if strings.Contains(src, ".Curate(") {
			t.Fatalf("%s must not invoke inline Curate — Tier 1 selected=0 only", rel)
		}
		if strings.Contains(src, "curationctx.TaskHint") || strings.Contains(src, "CurationTaskHint") {
			t.Fatalf("%s must not read user-prompt task hint", rel)
		}
	}
}

func TestSurveyArcSynthesisOneCuratePerBatch(t *testing.T) {
	t.Parallel()
	cur := &surveyArcCountingCurator{}
	block := strings.Repeat("x", survey.SynthesisEvidenceBudget+1)
	_, err := survey.MaybeCurateSynthesisEvidence(survey.EvidenceInput{
		Ctx:             context.Background(),
		ParentSessionID: "parent",
		Envelopes: []survey.WorkerEnvelope{
			{JobID: "j1", AgentType: "scout-a", Body: block, Report: survey.WorkerReportSnapshot{LegStatus: "complete"}},
			{JobID: "j2", AgentType: "scout-b", Body: block, Report: survey.WorkerReportSnapshot{LegStatus: "complete"}},
			{JobID: "j3", AgentType: "scout-c", Body: block, Report: survey.WorkerReportSnapshot{LegStatus: "complete"}},
		},
		MergedBytes: len(block) * 3,
		WorkerCount: 3,
		Reader:      surveyArcStubLedgerReader{},
		Curator:     cur,
		AllowCurate: true,
	})
	contractcheck.FailErr(t, "MaybeCurateSynthesisEvidence", err)
	if cur.calls != 1 {
		t.Fatalf("Curate calls = %d want 1 for N workers over budget", cur.calls)
	}
}

func TestSurveyArcSynthesisSnapshotIsolation(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	snapshot := evidence.AssembleLedger([]evidence.Record{
		{Handle: "grep#1", Kind: "grep", Survey: true, Path: "current.go", Body: []string{"5| current only"}},
	})
	provider := &stubCuratorProvider{
		id: "lite",
		responses: []string{
			`{"selections":[{"path":"prior.go","line":1,"excerpt":"prior survey"}],"gloss":[]}`,
		},
	}
	cur, err := llm.NewTestRegistrySummarizer(provider)
	contractcheck.FailErr(t, "NewTestRegistrySummarizer", err)
	got, err := cur.Curate(context.Background(), snapshot, llm.CurationFocus{
		Tool: "synthesis", View: "batch", Target: "parent",
		Task: "synthesis: rank worker evidence",
	}, survey.SynthesisCurateBudget)
	contractcheck.FailErr(t, "Curate", err)
	if len(got.Selections) != 0 {
		t.Fatalf("selection outside worker union snapshot must drop: %#v", got.Selections)
	}

	child := evidence.AssembleLedger([]evidence.Record{
		{Handle: "grep#1", Kind: "grep", Survey: true, Path: "a.go", Body: []string{"line"}},
		{Handle: "read#1", Kind: "read", Survey: false, Path: "b.go", Body: []string{"literal"}},
	})
	reader := surveyArcStubLedgerReader{ledgers: map[string]evidence.Ledger{"child-1": child}}
	snap, _, err := survey.BuildSynthesisSnapshot(context.Background(), survey.SnapshotInput{
		Envelopes: []survey.WorkerEnvelope{{
			JobID: "j1", ChildSessionID: "child-1", AgentType: "scout",
			Report: survey.WorkerReportSnapshot{LegStatus: "complete"},
		}},
		MergedBytes: 100,
		WorkerCount: 1,
		Reader:      reader,
	})
	contractcheck.FailErr(t, "BuildSynthesisSnapshot", err)
	if _, ok := snap.Handles["grep#1"]; !ok {
		var grepSeen bool
		for _, rec := range snap.Handles {
			if rec.Kind == "grep" && rec.Survey {
				grepSeen = true
				break
			}
		}
		if !grepSeen {
			t.Fatal("expected survey grep in worker union snapshot")
		}
	}
	var literalRead bool
	for _, rec := range snap.Handles {
		if rec.Kind == "read" && !rec.Survey {
			literalRead = true
			break
		}
	}
	if literalRead {
		t.Fatal("literal read must not enter synthesis snapshot")
	}
}

func TestSurveyArcSurveyRepoNoPreSessionLedger(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range []string{
		"lycaon/internal/survey/repo_tool.go",
		"lycaon/internal/survey/runner.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		src := string(body)
		for _, forbidden := range []string{"LoadLedger", "EvidenceLedgerReader", "UnionLegEvidence", "UnionCloseoutEvidence"} {
			if strings.Contains(src, forbidden) {
				t.Fatalf("%s must not resolve pre-session handles via %s", rel, forbidden)
			}
		}
	}
}

func TestSurveyArcSynthesisPassthroughUnderBudget(t *testing.T) {
	t.Parallel()
	cur := &surveyArcCountingCurator{}
	out, err := survey.MaybeCurateSynthesisEvidence(survey.EvidenceInput{
		Ctx:             context.Background(),
		ParentSessionID: "parent",
		Envelopes: []survey.WorkerEnvelope{{
			JobID: "j1", AgentType: "scout", Body: strings.Repeat("x", 100),
			Report: survey.WorkerReportSnapshot{LegStatus: "complete"},
		}},
		MergedBytes:    100,
		WorkerCount:    1,
		Reader:         surveyArcStubLedgerReader{},
		Curator:        cur,
		TopologyOutput: "topology unchanged",
		AllowCurate:    true,
	})
	contractcheck.FailErr(t, "MaybeCurateSynthesisEvidence", err)
	if out.Curated || out.EvidenceDigest != "" {
		t.Fatalf("under budget must passthrough: %+v", out)
	}
	if out.TopologyOutput != "topology unchanged" {
		t.Fatalf("topology = %q want unchanged passthrough", out.TopologyOutput)
	}
	if cur.calls != 0 {
		t.Fatalf("Curate calls = %d want 0 under budget", cur.calls)
	}
}

func TestSurveyArcKickPassthroughWithoutEvidenceDigest(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	base := map[string]any{
		"topology_output": "Worker A mapped routes.\n\n---\n\nWorker B checked tests.",
		"batch_phase":     "integrate",
	}
	contractcheck.FailErr(t, "merge kick policy vars", prompts.MergeCoordinatorKickPolicyVars(base))

	render := func(kickID string, data map[string]any) string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := engine.RenderKick(ctx, kickID, data)
		contractcheck.FailErr(t, "RenderKick "+kickID, err)
		return out
	}

	for _, kickID := range []string{
		"coordinator-topology-synthesis",
		"coordinator-security-synthesis",
		"coordinator-security-claims",
		"coordinator-security-challenge",
		"coordinator-worker-task-finished",
	} {
		t.Run(strings.TrimPrefix(kickID, "coordinator-"), func(t *testing.T) {
			t.Parallel()
			data := map[string]any{}
			for k, v := range base {
				data[k] = v
			}
			without := render(kickID, data)
			data["evidence_digest"] = ""
			withEmpty := render(kickID, data)
			if without != withEmpty {
				t.Fatalf("empty evidence_digest must not alter kick bytes\n--- without ---\n%s\n--- empty ---\n%s", without, withEmpty)
			}
		})
	}
}

func TestSurveyArcSynthesisCurateTaskFromBriefNotUserPrompt(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon/internal/survey/synthesis_curate.go")
	body, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read synthesis_curate.go", err)
	src := string(body)
	if strings.Contains(src, "curationctx") {
		t.Fatal("synthesis curate must not import curationctx — task from delegation brief or defaultSynthesisCurateTask")
	}
	if !strings.Contains(src, "resolveSynthesisCurateTask") || !strings.Contains(src, "defaultSynthesisCurateTask") {
		t.Fatal("synthesis curate must resolve task via resolveSynthesisCurateTask/defaultSynthesisCurateTask")
	}

	cur := &surveyArcFocusCurator{}
	snapshot := evidence.AssembleLedger([]evidence.Record{
		{Handle: "worker-j1-body", Kind: "worker_report", Survey: true, Body: []string{"body: survey notes"}},
	})
	_, _, err = survey.CurateSynthesisEvidence(
		context.Background(), cur, snapshot, "parent", "rank API surface drift", "investigate closeout", nil,
	)
	contractcheck.FailErr(t, "CurateSynthesisEvidence", err)
	if cur.lastFocus.Task != "rank API surface drift" {
		t.Fatalf("task = %q want delegation brief", cur.lastFocus.Task)
	}
}

func TestSurveyArcDigestDoesNotBypassCloseoutGrounding(t *testing.T) {
	t.Parallel()
	ev := guidance.CloseoutEvidence{
		Ledger: evidenceWithObservedPaths("src/a.go"),
	}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Digest present but path fabricated.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "lycaon-den/src/transport/", Line: 1, Excerpt: "package transport",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev)
	if eval.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("code=%q want %q — evidence_digest must not bypass cited_evidence grounding", eval.Code, guidance.SynthHandleNotInLegsCode)
	}
}

func TestSurveyArcSurveyRepoEvidenceSurveyGrade(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon/internal/evidence/fingerprint.go")
	body, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read fingerprint.go", err)
	if !strings.Contains(string(body), `case "survey_repo":`) {
		t.Fatal("survey_repo must be registered in BuildEvidenceRecord for survey-grade capture")
	}
}

func TestSurveyArcObservabilitySlogFields(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	obsPath := filepath.Join(root, "lycaon/internal/survey/survey_obs.go")
	body, err := os.ReadFile(obsPath)
	contractcheck.FailErr(t, "read survey_obs.go", err)
	src := string(body)
	for _, key := range []string{
		`survey_repo_calls`,
		`synthesis_curate_triggered`,
		`synthesis_curate_selected_total`,
	} {
		if !strings.Contains(src, key) {
			t.Fatalf("survey_obs.go missing slog field %q", key)
		}
	}
}

func TestSurveyArcNoStaleOrientOnlyPromptRefs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	promptsDir := filepath.Join(root, "lycaon/config/packs")
	var hits []string
	err := filepath.Walk(promptsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return walkErr
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(body)
		if strings.Contains(src, "survey_repo") {
			return nil
		}
		if strings.Contains(src, "`orient`") || strings.Contains(src, "call orient") {
			hits = append(hits, strings.TrimPrefix(path, root+string(filepath.Separator)))
		}
		return nil
	})
	contractcheck.FailErr(t, "walk prompts", err)
	if len(hits) > 0 {
		t.Fatalf("orient-only prompt refs without survey_repo mention:\n%s", strings.Join(hits, "\n"))
	}
}

func TestSurveyArcRepoToolSelectedZeroConstant(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon/internal/survey/repo_tool.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse repo_tool.go", err)
	var sawSelectedZero bool
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		st, ok := cl.Type.(*ast.Ident)
		if !ok || st.Name != "repoResponse" {
			return true
		}
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Selected" {
				continue
			}
			lit, ok := kv.Value.(*ast.BasicLit)
			if ok && lit.Kind == token.INT && lit.Value == "0" {
				sawSelectedZero = true
			}
		}
		return true
	})
	if !sawSelectedZero {
		t.Fatal("buildRepoResponse must pin Selected: 0 (Tier 1 no inline curate)")
	}
}

type surveyArcCountingCurator struct {
	calls int
}

func (c *surveyArcCountingCurator) Curate(_ context.Context, _ evidence.Ledger, _ llm.CurationFocus, _ int) (llm.CurationResult, error) {
	c.calls++
	return llm.CurationResult{
		Selections: []llm.MaterializedSelection{{
			Triple:     evidence.Triple{Path: "a.go", Line: 1, Excerpt: "x"},
			Resolution: evidence.Resolution{Handle: "grep#1", Path: "a.go", Line: 1},
			Lines:      []string{"x"},
		}},
		Report: llm.CurationReport{Selected: 1, Total: 1},
	}, nil
}

type surveyArcFocusCurator struct {
	lastFocus llm.CurationFocus
}

func (c *surveyArcFocusCurator) Curate(_ context.Context, _ evidence.Ledger, focus llm.CurationFocus, _ int) (llm.CurationResult, error) {
	c.lastFocus = focus
	return llm.CurationResult{Report: llm.CurationReport{Selected: 0, Total: 1}}, nil
}

type surveyArcStubLedgerReader struct {
	ledgers map[string]evidence.Ledger
}

func (s surveyArcStubLedgerReader) LoadLedger(_ context.Context, sessionID string) (evidence.Ledger, error) {
	if s.ledgers == nil {
		return evidence.InitLedger(), nil
	}
	return s.ledgers[sessionID], nil
}

func evidenceWithObservedPaths(paths ...string) evidence.Ledger {
	var recs []evidence.Record
	for i, path := range paths {
		recs = append(recs, evidence.Record{
			Handle:     fmt.Sprintf("read#%d", i+1),
			Kind:       "read",
			Survey:     false,
			Path:       path,
			LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
		})
	}
	return evidence.AssembleLedger(recs)
}
