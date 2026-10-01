package api_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

var fixtureTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var (
	roundTripTime        = fixtureTime
	roundTripStartedAt   = fixtureTime.Add(time.Minute)
	roundTripCompletedAt = fixtureTime.Add(2 * time.Minute)
)

type wireRoundTripCase struct {
	name string
	val  any
}

var wireRoundTripCases = []wireRoundTripCase{
	{"Session", api.Session{
		ID:            "11111111-1111-4111-8111-111111111111",
		WorkspacePath: "/tmp/proj",
		Posture:       api.SessionPostureSpec,
		Status:        api.SessionStatusIdle,
		CreatedAt:     roundTripTime,
		UpdatedAt:     roundTripTime,
	}},
	{"CreateSessionRequest", api.CreateSessionRequest{
		ProjectID: "11111111-1111-4111-8111-111111111111",
		Posture:   api.SessionPostureOrchestrate,
	}},
	{"PromptRequest", api.PromptRequest{Text: "hello"}},
	{"AbortSessionRequest", api.AbortSessionRequest{Reason: "user stopped"}},
	{"PromptAcceptedResponse", api.PromptAcceptedResponse{Status: "accepted"}},
	{"Message", api.Message{
		ID:            "m1",
		Role:          api.MessageRoleAssistant,
		Content:       "hi",
		Kind:          api.MessageKindWorkflowBoundary,
		WorkflowRunID: "run-1",
		WorkflowBoundary: &api.WorkflowBoundaryMeta{
			Event:      "started",
			WorkflowID: "plan",
		},
		ToolCalls: []api.ToolCall{{
			Name: "read",
			ID:   "tc1",
			Args: map[string]any{"path": "x"},
		}},
		CreatedAt: roundTripTime,
	}},
	{"Project", api.Project{
		ID:           "22222222-2222-4222-8222-222222222222",
		Roots:        []api.ProjectRoot{{ID: "root-1", Path: "/tmp/proj", Label: "proj", IsPrimary: true, AddedAt: roundTripTime, Kind: "attached"}},
		LastOpenedAt: roundTripTime,
		CreatedAt:    roundTripTime,
	}},
	{"SecurityOverview", api.SecurityOverview{
		ProjectID: "project-1", Enabled: true, Coverage: api.ScanCoverageBounded,
		Baseline: &api.SecurityBaseline{SnapshotID: "snap-1", CreatedAt: roundTripTime, FileCount: 12, UnobservedDirectories: 1},
		LastFull: &api.SecurityFullPass{AssessmentID: "assessment-1", StartedAt: &roundTripTime, CoverageStatus: api.ScanCoverageBounded, Trigger: api.ScanTriggerManual},
		Scanners: []api.SecurityScannerState{{ID: "lycaon-sast", Label: "Code analysis", Categories: []api.ScanCategory{api.ScanCategorySAST}, Available: true}},
	}},
	{"SecurityScannersSettingsResponse", api.SecurityScannersSettingsResponse{
		Enabled:           true,
		MergedFrom:        []string{"bundled"},
		LandedChangeScope: api.LandedChangeScopePathScoped,
	}},
	{"UpdateSecurityScannersSettingsRequest", api.UpdateSecurityScannersSettingsRequest{
		Enabled:           true,
		LandedChangeScope: api.LandedChangeScopeFullRoot,
	}},
	{"Blueprint", api.Blueprint{
		ProjectID: "22222222-2222-4222-8222-222222222222",
		Title:     "plan-a",
		Path:      settingsoverlay.Rel("blueprints/plan-a.md"),
		Content:   "# Goal",
		Status:    api.BlueprintStatusDraft,
		Version:   1,
		UpdatedAt: roundTripTime,
	}},
	{"Delegation", api.Delegation{
		ID:          "44444444-4444-4444-8444-444444444444",
		Task:        "fix bug",
		Strategy:    api.HuntStrategyFileBased,
		InspectMode: api.InspectModeStandard,
		Status:      "active",
		Phase:       api.DelegationPhaseWorker,
		CreatedAt:   roundTripTime,
	}},
	{"Leg", api.Leg{
		ID:           "55555555-5555-4555-8555-555555555555",
		DelegationID: "44444444-4444-4444-8444-444444444444",
		Title:        "leg-1",
		Status:       api.LegStatusPending,
		Files:        []string{"a.go"},
		CreatedAt:    roundTripTime,
		StartedAt:    &roundTripStartedAt,
		CompletedAt:  &roundTripCompletedAt,
	}},
	{"WorkerTask", api.WorkerTask{
		ID:              "66666666-6666-4666-8666-666666666666",
		DelegationID:    "44444444-4444-4444-8444-444444444444",
		LegID:           "55555555-5555-4555-8555-555555555555",
		ParentSessionID: "11111111-1111-4111-8111-111111111111",
		AgentType:       "implementer",
		Status:          api.WorkerStatusRunning,
		SpawnReason:     api.SpawnReasonInitial,
		Brief:           "fixture",
		Files:           []string{"b.go"},
		Result:          &api.WorkerResult{Summary: "done"},
		CreatedAt:       roundTripTime,
	}},
	{"BoardView", api.BoardView{
		Summary: "All workers complete.",
		Repo: api.RepoBrief{
			Languages:   []string{"Go", "TypeScript"},
			FileCount:   142,
			GeneratedAt: roundTripTime,
		},
		Git: &api.BoardGitSlice{
			Available:     true,
			Branch:        "main",
			Dirty:         true,
			StagedCount:   1,
			UnstagedCount: 2,
		},
		Cost:            &api.CostSummary{EstimatedNanoUsd: 420_000_000, EstimateCoverage: api.CostEstimateComplete},
		PackContentHash: "hash",
		DetailLevel:     api.BoardDetailLevelCompact,
		Board:           "Now: 2026-06-09 12:00 UTC",
		BoardChars:      24,
		GeneratedAt:     roundTripTime.Format(time.RFC3339),
		NowLine:         "Now: 2026-06-09 12:00 UTC",
	}},
	{"CostSummary", api.CostSummary{
		Scope:            api.CostScopeSession,
		SessionID:        "11111111-1111-4111-8111-111111111111",
		EstimatedNanoUsd: 1_230_000_000,
		TokenTotals:      api.TokenTotals{Prompt: 100, Completion: 50},
		Coordinator:      api.CostBreakdown{EstimatedNanoUsd: 1_000_000_000, TokenTotals: api.TokenTotals{Prompt: 80, Completion: 40}},
		Workers:          api.CostBreakdown{EstimatedNanoUsd: 230_000_000, TokenTotals: api.TokenTotals{Prompt: 20, Completion: 10}, TaskCount: 1},
		Summarizer:       api.CostBreakdown{EstimatedNanoUsd: 0, TokenTotals: api.TokenTotals{}},
		EstimateCoverage: api.CostEstimateComplete,
		PricingProvenance: []api.CostPricingProvenance{{
			Source: "models-dev", PricedAt: &roundTripTime,
		}},
	}},
	{"CodeScan", api.CodeScan{
		ID:            "88888888-8888-4888-8888-888888888888",
		Categories:    []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySAST},
		ScannerID:     "lycaon-sast",
		Status:        api.CodeScanStatusComplete,
		FindingsCount: 3,
		Result:        json.RawMessage(`{"findings":[]}`),
		CreatedAt:     roundTripTime,
	}},
	{"EventEnvelope", api.EventEnvelope{
		V:           1,
		Topic:       api.EventTopicSession,
		PublishedAt: roundTripTime,
		Scope:       api.EventScope{Kind: api.EventScopeProject, ProjectID: "22222222-2222-4222-8222-222222222222"},
		Data:        json.RawMessage(`{"id":"s1","status":"idle"}`),
	}},
	{"SessionEvent", api.SessionEvent{
		ID:          "11111111-1111-4111-8111-111111111111",
		ProjectID:   "22222222-2222-4222-8222-222222222222",
		Action:      api.SessionEventActionUpdated,
		Status:      api.SessionStatusBusy,
		LastMessage: "working",
	}},
	{"ActivityEvent", api.ActivityEvent{
		ActivityID: "77777777-7777-4777-8777-777777777777",
		SessionID:  "11111111-1111-4111-8111-111111111111",
		Kind:       api.ActivityKindRunningTool,
		Status:     api.ActivityStatusActive,
		StartedAt:  roundTripTime,
		ToolName:   "verify",
		ToolCallID: "call-1",
	}},
	{"ErrorResponse", api.ErrorResponse{
		Code:            api.ApiErrorCodeInternalError,
		Message:         "boom",
		Details:         map[string]any{"field": "x"},
		Retryable:       true,
		SuggestedAction: "retry",
	}},
	{"WorkflowRun", api.WorkflowRun{
		ID:              "99999999-9999-4999-8999-999999999999",
		SessionID:       "11111111-1111-4111-8111-111111111111",
		WorkflowID:      "coordinator",
		WorkflowVersion: "1",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "compose",
		CreatedAt:       roundTripTime,
		UpdatedAt:       roundTripTime,
	}},
	{"SettingsLimitsPatch", api.SettingsLimitsPatch{
		MaxIterations:                new(25),
		OverlayPromoteMaxIterations:  new(30),
		MaxToolResultBytes:           new(65536),
		LLMTurnTimeoutMs:             new(10800000),
		CoordinatorHostTurnTimeoutMs: new(10800000),
		CoordinatorMaxSleepMs:        new(10800000),
		AwaitParentWorkersTimeoutMs:  new(10800000),
	}},
}

func TestWireDTOJSONRoundTrip(t *testing.T) {
	for _, tc := range wireRoundTripCases {
		t.Run(tc.name, func(t *testing.T) {
			orig := tc.val
			b, err := json.Marshal(orig)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			clone := reflect.New(reflect.TypeOf(orig)).Interface()
			if err := json.Unmarshal(b, clone); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(orig, reflect.ValueOf(clone).Elem().Interface()) {
				t.Fatalf("round-trip mismatch\norig=%#v\ngot=%#v", orig, reflect.ValueOf(clone).Elem().Interface())
			}
			assertSnakeCaseJSONKeys(t, string(b))
		})
	}
}

func TestWireDTOOmitemptyFields(t *testing.T) {
	now := fixtureTime
	b, err := json.Marshal(api.Session{
		ID:        "s1",
		Posture:   api.SessionPostureSpec,
		Status:    api.SessionStatusIdle,
		CreatedAt: now,
		UpdatedAt: now,
	})
	testutil.FailErr(t, "json.Marshal failed", err)
	raw := string(b)
	if strings.Contains(raw, "plan_path") {
		t.Fatalf("expected plan_path omitted, got %s", raw)
	}
}

func assertSnakeCaseJSONKeys(t *testing.T, jsonStr string) {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(jsonStr), &v); err != nil {
		t.Fatalf("parse json keys: %v", err)
	}
	walkJSONKeys(t, v, "")
}

func walkJSONKeys(t *testing.T, v any, prefix string) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if strings.Contains(k, "-") {
				t.Fatalf("json key %q uses kebab-case", joinKey(prefix, k))
			}
			if hasUpperASCII(k) {
				t.Fatalf("json key %q uses camelCase", joinKey(prefix, k))
			}
			walkJSONKeys(t, child, joinKey(prefix, k))
		}
	case []any:
		for i, child := range x {
			walkJSONKeys(t, child, prefix+"["+fmt.Sprint(i)+"]")
		}
	}
}

func joinKey(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

func hasUpperASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			return true
		}
	}
	return false
}
