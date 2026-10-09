//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type projectReportCountingTracker struct {
	cost.CostTracker
	summaryCalls int
	reportCalls  int
}

func (t *projectReportCountingTracker) Summary(ctx context.Context, scope wire.CostScope, sessionID, projectID string) (wire.CostSummary, error) {
	t.summaryCalls++
	return t.CostTracker.Summary(ctx, scope, sessionID, projectID)
}

func (t *projectReportCountingTracker) ProjectReport(ctx context.Context, projectID string, query cost.ReportQuery) (wire.ProjectCostReport, error) {
	t.reportCalls++
	return t.CostTracker.ProjectReport(ctx, projectID, query)
}

func TestHandleCostSummarySessionBreakdown(t *testing.T) {
	tracker := costtest.NewTracker(t, nil)
	store := store.NewMemory()
	mgr := session.NewManagerWithLLMService(store, llm.NewMockProvider(nil), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits(), tracker)
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr}), nil, TestAPIToken)

	parent, err := store.Create(t.Context(), wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(t.Context(), parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "work"})
	testutil.FailErr(t, "store.CreateChild failed", err)

	coord := 0.40
	worker := 0.18
	if err := tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID: parent.ID, Caller: cost.CallerCoordinator, EstimatedNanoUSD: costtest.NanoUSD(t, coord),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID: child.ID, ParentSessionID: parent.ID, Caller: cost.CallerWorker, EstimatedNanoUSD: costtest.NanoUSD(t, worker),
	}); err != nil {
		t.Fatal(err)
	}

	req := newAuthedRequest(http.MethodGet, "/v1/cost/summary?session_id="+parent.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var summary wire.CostSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if summary.Coordinator.EstimatedNanoUsd != *costtest.NanoUSD(t, coord) || summary.Workers.EstimatedNanoUsd != *costtest.NanoUSD(t, worker) {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestHandleCostSummaryProjectBreakdown(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	tracker := costtest.NewTracker(t, nil)
	store := store.NewMemory()
	mgr := session.NewManagerWithLLMService(store, llm.NewMockProvider(nil), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits(), tracker)
	reg := project.NewMemoryRegistry()
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store, Projects: reg, Sessions: mgr}), nil, TestAPIToken)

	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open failed", err)
	parent, err := store.Create(t.Context(), wire.CreateSessionRequest{Posture: wire.SessionPostureBuild, ProjectID: p.ID}, "")
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(t.Context(), parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "work"})
	testutil.FailErr(t, "store.CreateChild failed", err)

	c1, c2 := 0.10, 0.07
	if err := tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID: parent.ID, ProjectID: p.ID, Caller: cost.CallerCoordinator, EstimatedNanoUSD: costtest.NanoUSD(t, c1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID: child.ID, ParentSessionID: parent.ID, ProjectID: p.ID, Caller: cost.CallerWorker, EstimatedNanoUSD: costtest.NanoUSD(t, c2),
	}); err != nil {
		t.Fatal(err)
	}

	req := newAuthedRequest(http.MethodGet, "/v1/cost/summary?project_id="+p.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var summary wire.CostSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if summary.Coordinator.EstimatedNanoUsd != *costtest.NanoUSD(t, c1) || summary.Workers.EstimatedNanoUsd != *costtest.NanoUSD(t, c2) {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestHandleProjectCostReportIncludesSessionBreakdownAndArchivedChats(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "cost-report.db")
	tracker := &projectReportCountingTracker{CostTracker: cost.NewSQLTracker(sqlDB, nil)}
	memStore := store.NewSQL(sqlDB)
	mgr := session.NewManagerWithLLMService(memStore, llm.NewMockProvider(nil), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits(), tracker)
	reg := project.NewSQLRegistry(sqlDB)
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: memStore, Projects: reg, Sessions: mgr}), nil, TestAPIToken)

	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "create project", err)
	first, err := memStore.Create(t.Context(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild, ProjectID: p.ID,
	}, "")
	testutil.FailErr(t, "create first session", err)
	child, err := memStore.CreateChild(t.Context(), first, wire.SpawnChildRequest{
		AgentType: "implementer", Prompt: "work",
	})
	testutil.FailErr(t, "create worker child", err)
	second, err := memStore.Create(t.Context(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureVet, ProjectID: p.ID,
	}, "")
	testutil.FailErr(t, "create second session", err)
	_, err = mgr.Chats.SetArchived(t.Context(), second.ID, true)
	testutil.FailErr(t, "archive second session", err)

	firstUSD, childUSD, secondUSD, utilityUSD, retiredUSD := 0.40, 0.10, 0.20, 0.05, 0.15
	testutil.FailErr(t, "record first session usage", tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID: first.ID, ProjectID: p.ID, Caller: cost.CallerCoordinator,
		EstimatedNanoUSD: costtest.NanoUSD(t, firstUSD),
	}))
	testutil.FailErr(t, "record worker usage", tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID: child.ID, ParentSessionID: first.ID, ProjectID: p.ID,
		Caller: cost.CallerWorker, EstimatedNanoUSD: costtest.NanoUSD(t, childUSD),
	}))
	testutil.FailErr(t, "record archived session usage", tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID: second.ID, ProjectID: p.ID, Caller: cost.CallerCoordinator,
		EstimatedNanoUSD: costtest.NanoUSD(t, secondUSD),
	}))
	testutil.FailErr(t, "record project utility usage", tracker.RecordUsage(t.Context(), cost.UsageEvent{
		ProjectID: p.ID, Caller: cost.CallerSummarizer, EstimatedNanoUSD: costtest.NanoUSD(t, utilityUSD),
	}))
	testutil.FailErr(t, "record retired session usage", tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID: "deleted-session", ProjectID: p.ID, Caller: cost.CallerCoordinator,
		EstimatedNanoUSD: costtest.NanoUSD(t, retiredUSD),
	}))

	req := newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/cost-report", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var report wire.ProjectCostReport
	testutil.FailErr(t, "decode project cost report", json.Unmarshal(w.Body.Bytes(), &report))
	nano := func(usd float64) int64 { return *costtest.NanoUSD(t, usd) }
	projectTotal := nano(firstUSD) + nano(childUSD) + nano(secondUSD) + nano(utilityUSD) + nano(retiredUSD)
	if report.Summary.ProjectID != p.ID || report.Summary.EstimatedNanoUsd != projectTotal {
		t.Fatalf("project summary = %+v", report.Summary)
	}
	if report.ProjectUtilities.ProjectID != p.ID || report.ProjectUtilities.EstimatedNanoUsd != nano(utilityUSD) {
		t.Fatalf("project utilities = %+v", report.ProjectUtilities)
	}
	if report.RetiredSessions.ProjectID != p.ID || report.RetiredSessions.EstimatedNanoUsd != nano(retiredUSD) {
		t.Fatalf("retired sessions = %+v", report.RetiredSessions)
	}
	if len(report.Sessions) != 2 {
		t.Fatalf("session rows = %d want 2", len(report.Sessions))
	}
	if tracker.reportCalls != 1 || tracker.summaryCalls != 0 {
		t.Fatalf("cost queries = ProjectReport:%d Summary:%d want one batched report", tracker.reportCalls, tracker.summaryCalls)
	}
	byID := make(map[string]wire.ProjectCostSession, len(report.Sessions))
	for _, row := range report.Sessions {
		byID[row.Session.ID] = row
	}
	if byID[first.ID].Cost.EstimatedNanoUsd != nano(firstUSD)+nano(childUSD) || byID[first.ID].Cost.Workers.EstimatedNanoUsd != nano(childUSD) {
		t.Fatalf("first session cost = %+v", byID[first.ID].Cost)
	}
	if byID[second.ID].Session.ArchivedAt == nil || byID[second.ID].Cost.EstimatedNanoUsd != nano(secondUSD) {
		t.Fatalf("archived session row = %+v", byID[second.ID])
	}
}
