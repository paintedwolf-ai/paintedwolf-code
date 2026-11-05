package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type storeLedgerReader struct {
	store Store
}

func (r storeLedgerReader) LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error) {
	return r.store.LoadLedger(ctx, sessionID)
}

type evidenceRowSnapshot struct {
	Handle            string
	Ordinal           int
	Kind              string
	Shape             string
	TrustTier         string
	SourceTool        string
	Path              string
	URL               string
	LineRanges        string
	ContentBlobSha256 string
	Truncated         int
	Survey            int
	SupersededBy      sql.NullString
}

func queryEvidenceRows(t *testing.T, database db.Handle, sessionID string) []evidenceRowSnapshot {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `
		SELECT handle, ordinal, kind, shape, fidelity, source_tool, path, url, line_ranges, content_blob_sha256, truncated, survey, superseded_by
		FROM evidence_records
		WHERE session_id = ?
		ORDER BY handle ASC
	`, sessionID)
	testutil.FailErr(t, "query evidence_records", err)
	t.Cleanup(func() { _ = rows.Close() })
	var out []evidenceRowSnapshot
	for rows.Next() {
		var snap evidenceRowSnapshot
		testutil.FailErr(t, "scan evidence row", rows.Scan(
			&snap.Handle, &snap.Ordinal, &snap.Kind, &snap.Shape, &snap.TrustTier, &snap.SourceTool,
			&snap.Path, &snap.URL, &snap.LineRanges, &snap.ContentBlobSha256, &snap.Truncated, &snap.Survey, &snap.SupersededBy,
		))
		out = append(out, snap)
	}
	testutil.FailErr(t, "evidence rows iteration", rows.Err())
	return out
}

type groundingAuditSnapshot struct {
	WorkerSummaryStatus string
	WorkerCitationCode  string
	InvestigateCode     string
	SynthesisCode       string
	UnverifiableCode    string
}

func runGroundingAudits(t *testing.T, ctx context.Context, store Store, sessionID, projectDir string) groundingAuditSnapshot {
	t.Helper()
	ledger := storeLedgerReader{store: store}
	ev, err := store.LoadLedger(ctx, sessionID)
	testutil.FailErr(t, "LoadLedger", err)

	workerEval, workerEvalErr := workercompletion.EvaluateWorkerSummary(ctx, workercompletion.WorkerSummaryEvalInput{
		AgentType:      orchestration.ProfileCodeReviewer,
		ChildSessionID: sessionID,
		ProjectDir:     projectDir,
		Ledger:         ledger,
		Report: workercompletion.WorkerCompletionReport{
			Brief: "Reviewed src/a.go",
			Findings: []workercompletion.WorkerFinding{{
				Path: "src/a.go", Line: 1, Excerpt: "package a",
			}},
		},
	})
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	workerCitation := guidance.EvaluateWorkerCitations(
		evidence.CitationRoots{ProjectDir: projectDir},
		[]guidance.WorkerFindingInput{{Path: "src/a.go", Line: 1, Excerpt: "package a"}},
		nil,
		guidance.WorkerNarrativeInput{},
		ev,
	)
	coordReport := guidance.CoordinatorCompletionReport{
		Synthesis: "Finding in src/a.go",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/a.go", Line: 1, Excerpt: "package a",
		}},
	}
	investigate := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: projectDir}, "implement_investigate", coordReport, guidance.CloseoutEvidence{Ledger: ev})
	synth := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: projectDir}, "implement_synthesis", coordReport, guidance.CloseoutEvidence{
		Ledger: evidence.NamespaceLedger(ev, "leg-a"),
	})
	unverifiable := guidance.EvaluateWorkerCitations(
		evidence.CitationRoots{ProjectDir: projectDir},
		[]guidance.WorkerFindingInput{{Path: "internal/phantom.go", Line: 1, Excerpt: "fabricated"}},
		nil,
		guidance.WorkerNarrativeInput{},
		ev,
	)
	return groundingAuditSnapshot{
		WorkerSummaryStatus: workerEval.Status,
		WorkerCitationCode:  workerCitation.Code,
		InvestigateCode:     investigate.Code,
		SynthesisCode:       synth.Code,
		UnverifiableCode:    unverifiable.Code,
	}
}

func newSQLCompactionManager(t *testing.T, store Store, cfg compaction.CompactionConfig) *Host {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	mgr := NewHost(store, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(cfg, compaction.MockSummarizer{Text: "Continue from compacted context."}))
	return mgr
}

// TestEvidenceLedgerCompactionIndependence proves compaction leaves evidence_records
// byte-identical and grounding audits stable.
func TestEvidenceLedgerCompactionIndependence(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := store.NewSQL(sqlDB)
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ChunkTokenThreshold = 500
	cfg.ChunkMinSavingsTokens = 100
	mgr := newSQLCompactionManager(t, store, cfg)

	ctx := context.Background()
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	srcPath := filepath.Join(projectDir, "src", "a.go")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(srcPath, []byte("package a\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	readJSON := `{"path":"src/a.go","content":"1|package a","offset":1,"end_line":1,"limit":1}`
	handle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, projectDir, "read", map[string]any{"path": "src/a.go"}, readJSON)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)
	if handle != "read#1" {
		t.Fatalf("handle = %q want read#1", handle)
	}

	beforeRows := queryEvidenceRows(t, sqlDB, sess.ID)
	beforeAudits := runGroundingAudits(t, ctx, store, sess.ID, projectDir)

	huge := strings.Repeat("ERROR: build failed\n", 8000)
	if err := store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Content: "survey"},
		api.Message{ID: "t-big", Role: api.MessageRoleTool, Content: huge},
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, Content: "seen"},
	); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Runner.History.ScheduleChunks(ctx, sess); err != nil {
		testutil.FailErr(t, "compactOversizedToolResultsInSession", err)
	}
	mgr.Runner.History.Wait()
	// Messages stay canonical; the compacted chunk lives only in the applied view.
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	for _, m := range msgs {
		if m.ID == "t-big" && m.Content != huge {
			t.Fatal("canonical tool message was mutated by compaction")
		}
	}
	view := appliedView(t, mgr, store, sess.ID)
	compacted := false
	for _, m := range view {
		if m.CompactedChunk != nil {
			compacted = true
			break
		}
	}
	if !compacted {
		t.Fatal("expected at least one compacted tool chunk in the view")
	}

	afterRows := queryEvidenceRows(t, sqlDB, sess.ID)
	if fmt.Sprint(beforeRows) != fmt.Sprint(afterRows) {
		t.Fatalf("evidence_records changed after compaction\nbefore: %+v\nafter:  %+v", beforeRows, afterRows)
	}

	afterAudits := runGroundingAudits(t, ctx, store, sess.ID, projectDir)
	if beforeAudits != afterAudits {
		t.Fatalf("audit outcomes changed after compaction\nbefore: %+v\nafter:  %+v", beforeAudits, afterAudits)
	}
	if afterAudits.WorkerSummaryStatus != "complete" {
		t.Fatalf("worker summary status = %q want complete", afterAudits.WorkerSummaryStatus)
	}
	if afterAudits.UnverifiableCode != guidance.WorkerEvidenceHandleUnknownCode {
		t.Fatalf("unverifiable code = %q want %q", afterAudits.UnverifiableCode, guidance.WorkerEvidenceHandleUnknownCode)
	}
}

func advisoryTestManager(t *testing.T) *Host {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	mgr, _ := newTestManager(t)
	mgr.SetRejectFormatter(rejectFmt)
	mgr.SetOARPipeline(nil, oar.NewRenderer(rejectFmt, nil))
	return mgr
}

func TestQueueCoordinatorGuidanceAdvisoriesStacksDistinctCodes(t *testing.T) {
	mgr := advisoryTestManager(t)
	ctx := t.Context()
	mgr.Coordinator.Guidance.QueueAdvisories(ctx, "sess-adv", []guidance.GuidanceNudge{
		{Code: "COORDINATOR_UNGROUNDED_CLAIM"},
		{Code: "COORDINATOR_HOST_TURN_REQUIRES_WAIT"},
	})
	first := mgr.Coordinator.Runtime.Kicks().TakePendingKickID("sess-adv")
	if first != "guidance:COORDINATOR_UNGROUNDED_CLAIM:session:sess-adv" {
		t.Fatalf("first kick = %q", first)
	}
	text, lease, ok, _ := mgr.Coordinator.Runtime.Kicks().RenderPendingNudge(ctx, "sess-adv", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "Code: COORDINATOR_UNGROUNDED_CLAIM") {
		t.Fatalf("first nudge = %q ok=%v", text, ok)
	}
	mgr.Coordinator.Runtime.Kicks().AckPendingNudge("sess-adv", lease)
	second := mgr.Coordinator.Runtime.Kicks().TakePendingKickID("sess-adv")
	if second != "guidance:COORDINATOR_HOST_TURN_REQUIRES_WAIT:session:sess-adv" {
		t.Fatalf("second kick = %q", second)
	}
	text, _, ok, _ = mgr.Coordinator.Runtime.Kicks().RenderPendingNudge(ctx, "sess-adv", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "Code: COORDINATOR_HOST_TURN_REQUIRES_WAIT") {
		t.Fatalf("second nudge = %q ok=%v", text, ok)
	}
}

func TestRenderOARResultConcatenatesWarnAdvisories(t *testing.T) {
	mgr := advisoryTestManager(t)
	res := &oar.PipelineResult{
		Enforced: true,
		Decision: &oar.Decision{
			Effect: oar.EffectWarn,
			Code:   "DOOM_LOOP_REPEAT_WARN",
			Advisories: []oar.Advisory{
				{Code: "DOOM_LOOP_REPEAT_WARN"},
				{Code: "DOOM_LOOP_FRUITLESS_SEARCH"},
			},
		},
	}
	refuse, blocked, err := mgr.Coordinator.Feedback.RenderResult(context.Background(), oar.AnchorToolPost, res)
	testutil.FailErr(t, "renderOARResult", err)
	if blocked || refuse == nil {
		t.Fatalf("blocked=%v refuse=%v", blocked, refuse)
	}
	if refuse.Code() != "DOOM_LOOP_REPEAT_WARN" {
		t.Fatalf("envelope code = %q, want first advisory", refuse.Code())
	}
	if !strings.Contains(refuse.Body, "DOOM_LOOP_REPEAT_WARN") || !strings.Contains(refuse.Body, "DOOM_LOOP_FRUITLESS_SEARCH") {
		t.Fatalf("body dropped a warn advisory: %q", refuse.Body)
	}
}

func TestErrGroundingNudgeSingleCodeQueues(t *testing.T) {
	mgr := advisoryTestManager(t)
	nudge := &guidance.ErrGroundingNudge{Code: "COORDINATOR_UNGROUNDED_CLAIM"}
	mgr.Coordinator.Guidance.QueueAdvisories(t.Context(), "sess-one", nudge.Nudges())
	if id := mgr.Coordinator.Runtime.Kicks().TakePendingKickID("sess-one"); id != "guidance:COORDINATOR_UNGROUNDED_CLAIM:session:sess-one" {
		t.Fatalf("kick id = %q", id)
	}
}

// [OAR-COPY-1] A queued grounding warning must keep the evaluated branch.
func TestGroundingNudgeQueuesFrozenCopy(t *testing.T) {
	mgr := advisoryTestManager(t)
	nudge := &guidance.ErrGroundingNudge{Code: "COORDINATOR_UNGROUNDED_CLAIM", Data: map[string]any{"count": 99}, Copy: map[string]string{"what": "Measured 3 claims", "fix": "Keep literal {{ count }}"}}
	mgr.Coordinator.Guidance.QueueAdvisories(t.Context(), "frozen", nudge.Nudges())
	if mgr.Coordinator.Runtime.Kicks().TakePendingKickID("frozen") == "" {
		t.Fatal("frozen nudge was not queued")
	}
	text, _, ok, err := mgr.Coordinator.Runtime.Kicks().RenderPendingNudge(t.Context(), "frozen", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render frozen grounding warning", err)
	if !ok || !strings.Contains(text, "Measured 3 claims") || !strings.Contains(text, "{{ count }}") || strings.Contains(text, "Rejected:") {
		t.Fatalf("lost frozen advisory: %s", text)
	}
}

// [OAR-COPY-1] A guard conversion must keep both the evaluated display and facts.
func TestGuardRefusalPreservesFrozenCopy(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	nudge := &guidance.ErrGroundingNudge{Code: "COORDINATOR_UNGROUNDED_CLAIM", Data: map[string]any{"count": 99}, Copy: map[string]string{"what": "Measured 3 claims", "fix": "Retain {{ count }} literally"}}
	reject := policyfacts.RejectFromGuardErr(t.Context(), nudge)
	if reject == nil || !strings.Contains(reject.Body, "Measured 3 claims") || !strings.Contains(reject.Body, "{{ count }}") || !errors.Is(reject, nudge) {
		t.Fatalf("guard conversion lost frozen delivery or cause: %#v", reject)
	}
}

// activeRunView supplies a fixed active run for event attribution.
type activeRunView struct {
	recordingWorkflowView
	run *api.WorkflowRun
}

func (v *activeRunView) ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	v.record("ActiveBySession")
	return v.run, nil
}

func newHostEventManager(t *testing.T, run *api.WorkflowRun) (*Host, string) {
	t.Helper()
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	workflowFixture1 := &activeRunView{run: run}
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture1, Policy: workflowFixture1, Ambient: workflowFixture1, Blueprints: workflowFixture1, Batch: workflowFixture1, Slash: workflowFixture1, Requests: workflowFixture1, Feedback: workflowFixture1, Transcript: workflowFixture1, Asks: workflowFixture1, Fanout: workflowFixture1, Phases: workflowFixture1, Reports: workflowFixture1, Recovery: workflowFixture1, Cleanup: workflowFixture1})
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "store.Create failed", err)
	return mgr, sess.ID
}

// TestAppendHostEventStampedWithActiveRun verifies that an overlay
// promote/reject event appended while a run is active carries that run's id at
// creation, so it lands in its span with no client attribution fallback.
func TestAppendHostEventStampedWithActiveRun(t *testing.T) {
	const runID = "run-host-1"
	mgr, sessionID := newHostEventManager(t, &api.WorkflowRun{ID: runID})

	if err := mgr.ProjectControl.AppendHostEvent(context.Background(), sessionID, hostmarker.OverlayPromoteEventPrefix, map[string]any{"overlay": "o1"}, ""); err != nil {
		testutil.FailErr(t, "appendHostEvent failed", err)
	}
	msgs, err := mgr.Coordinator.Context.Sessions.(Store).GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages failed", err)
	var found bool
	for _, m := range msgs {
		if m.Role == api.MessageRoleTool && m.WorkflowRunID == runID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no host-event tool row stamped with run id %q in %d rows", runID, len(msgs))
	}
}

// TestAppendHostEventUnstampedWhenNoActiveRun confirms the legitimate no-run
// case leaves workflow_run_id empty rather than inventing one.
func TestAppendHostEventUnstampedWhenNoActiveRun(t *testing.T) {
	mgr, sessionID := newHostEventManager(t, nil)

	if err := mgr.ProjectControl.AppendHostEvent(context.Background(), sessionID, hostmarker.OverlayRejectEventPrefix, map[string]any{"overlay": "o2"}, ""); err != nil {
		testutil.FailErr(t, "appendHostEvent failed", err)
	}
	msgs, err := mgr.Coordinator.Context.Sessions.(Store).GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages failed", err)
	for _, m := range msgs {
		if m.Role == api.MessageRoleTool && m.WorkflowRunID != "" {
			t.Fatalf("host-event workflow_run_id = %q want empty (no active run)", m.WorkflowRunID)
		}
	}
}
