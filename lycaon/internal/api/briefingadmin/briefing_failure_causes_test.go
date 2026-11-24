package briefingadmin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/api/briefingadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefinghost"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type routeProvider struct {
	id    string
	calls atomic.Int64
	fn    func(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error)
}

func (p *routeProvider) ID() string { return p.id }
func (p *routeProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: p.id, ContextLength: 16384}}
}
func (p *routeProvider) Profile() providerprofile.Profile {
	return providerprofile.Profile{UtilitySingleFlight: true}
}
func (p *routeProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.calls.Add(1)
	return p.fn(ctx, req)
}
func (p *routeProvider) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, fmt.Errorf("unused")
}

type twoSectionTarget struct {
	Key     string `json:"key"`
	Purpose string `json:"purpose"`
	Change  string `json:"change"`
}

type twoSectionProfile struct{ policy briefing.Policy }

func (twoSectionProfile) ID() string                { return "tool_activity" }
func (twoSectionProfile) Version() string           { return "1" }
func (p twoSectionProfile) Policy() briefing.Policy { return p.policy }

func (p twoSectionProfile) Resolve(_ context.Context, target briefing.Target) (briefing.Request, error) {
	var ref twoSectionTarget
	if err := json.Unmarshal(target.Ref, &ref); err != nil || ref.Key == "" {
		return briefing.Request{}, fmt.Errorf("%w: key required", briefing.ErrInvalidTarget)
	}
	sec := func(id, text string) briefing.SectionPlan {
		return briefing.SectionPlan{
			ID:   id,
			Mode: briefing.Generated,
			Manifest: briefing.Manifest{
				ProfileID: "tool_activity", ProfileVersion: "1", SectionID: id,
				Template: briefing.Template{ID: id, Version: "1"}, Contract: briefing.OneSentence,
				Items: []briefing.Item{{Key: id, Role: briefing.RoleObservation, SourceKind: "receipt", Inclusion: briefing.IncludedWhole, Text: text}},
			},
			Prompt: briefing.Prompt{System: "Summarize.", User: text, MaxTokens: 50},
		}
	}
	return briefing.Request{
		ProjectID: target.ProjectID, ProjectDir: target.ProjectDir, ProfileID: p.ID(), Version: p.Version(),
		TargetKey: ref.Key, TargetKind: "activity", TargetJSON: string(target.Ref), ScopeFingerprint: ref.Key,
		Manual: target.Manual, Policy: p.policy,
		Sections: []briefing.SectionPlan{sec("purpose", ref.Purpose), sec("change", ref.Change)},
	}, nil
}

type eventHubCapture struct {
	mu        sync.Mutex
	snapshots chan wire.BriefingSnapshot
}

func (c *eventHubCapture) Publish(_ context.Context, topic wire.EventTopic, _ events.PublishKey, payload any) error {
	if topic != wire.EventTopicBriefing {
		return nil
	}
	if ev, ok := payload.(wire.BriefingEvent); ok && ev.Kind == "snapshot" && ev.Snapshot != nil {
		c.mu.Lock()
		c.snapshots <- *ev.Snapshot
		c.mu.Unlock()
	}
	return nil
}

func newRealBriefingStack(t *testing.T, lite, coord *routeProvider) (http.Handler, *project.Project, *llm.UtilityPlane, *eventHubCapture) {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "0")
	t.Setenv("LYCAON_LLM_MANUAL", "0")
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))

	deps := apitest.Dependencies(t, api.Dependencies{})
	rootDir := t.TempDir()
	p, err := projecttest.CreateWithRoot(t.Context(), deps.Core.Projects, rootDir)
	testutil.FailErr(t, "create project", err)

	reg := llm.NewEmptyRegistry()
	testutil.FailErr(t, "register lite", reg.Register(lite))
	testutil.FailErr(t, "register coord", reg.Register(coord))

	policyStore, err := llm.NewPolicyStoreAt(filepath.Join(t.TempDir(), "models.yaml"))
	testutil.FailErr(t, "policy store", err)
	testutil.FailErr(t, "set policy", policyStore.PutProject(rootDir, llm.ModelPolicy{
		Lite:        llm.ModelRef{ProviderID: lite.ID(), Model: lite.ID()},
		Coordinator: llm.ModelRef{ProviderID: coord.ID(), Model: coord.ID()},
	}))

	plane := llm.NewUtilityPlaneWithOverlayWait(time.Millisecond)
	service := &llm.Service{Registry: reg, Policy: policyStore, Utility: plane}
	generator := briefing.NewModelGenerator(service, nil)
	if generator == nil {
		t.Fatal("expected real model generator")
	}

	database := testdbfixture.Open(t, "briefing-e2e.db")
	testdbseed.InsertProjectRoot(t, database, p.ID, rootDir)
	host := briefinghost.New(context.Background(), database, t.TempDir(), generator)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	testutil.FailErr(t, "register profile", host.Registry.Register(twoSectionProfile{policy: briefing.QueuedPolicy}))

	hub := &eventHubCapture{snapshots: make(chan wire.BriefingSnapshot, 16)}
	host.Publish(hub, slog.Default())

	files := filebriefing.NewService(context.Background(), filebriefing.Dependencies{
		Store: filebriefing.NewSQL(deps.Core.Database), Settings: &switchable{enabled: true},
	})
	t.Cleanup(func() { files.Stop(); files.Wait(context.Background()) })

	h := briefingadmin.New(&httpio.Responder{Logger: slog.Default()}, briefingadmin.Dependencies{
		Briefings: briefingadmin.Services{Files: files, Host: host}, ProjectRegistry: deps.Core.Projects, SessionStore: deps.Core.Store.Sessions,
	})
	r := chi.NewRouter()
	r.Post("/v1/projects/{id}/briefings", h.Engine.HandleRequestBriefing)
	return r, p, plane, hub
}

func postBriefing(t *testing.T, router http.Handler, projectID string, target twoSectionTarget, trigger string) wire.BriefingSnapshot {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"profile_id": "tool_activity", "target_ref": target, "trigger": trigger})
	testutil.FailErr(t, "marshal request", err)
	req := httptest.NewRequest(http.MethodPost, "/v1/projects/"+projectID+"/briefings", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	return decode[wire.BriefingSnapshot](t, w)
}

func TestRealGeneratorBusySlotAndManualFallbackAcrossStoreAndSSE(t *testing.T) {
	lite := &routeProvider{id: "lite", fn: func(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
		if strings.Contains(req.Messages[1].Content, "fail-change") {
			return nil, fmt.Errorf("lite failed on change")
		}
		return &modelcall.Completion{Content: "Runs the test suite."}, nil
	}}
	coord := &routeProvider{id: "coord", fn: func(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
		return &modelcall.Completion{Content: "All checks passed cleanly."}, nil
	}}

	router, proj, plane, hub := newRealBriefingStack(t, lite, coord)

	// Hold the lite single-flight lane so automatic overlay admission is refused immediately with zero provider calls.
	releaseLane, err := plane.AcquireForeground(t.Context(), lite)
	testutil.FailErr(t, "hold lite lane", err)

	target := twoSectionTarget{Key: "cmd-1", Purpose: "run-tests", Change: "fail-change"}
	pending := postBriefing(t, router, proj.ID, target, "automatic")
	if pending.Status != "pending" {
		t.Fatalf("expected pending snapshot, got %+v", pending)
	}

	failedSSE := <-hub.snapshots
	if lite.calls.Load() != 0 || coord.calls.Load() != 0 {
		t.Fatalf("busy slot invoked provider: lite=%d coord=%d", lite.calls.Load(), coord.calls.Load())
	}
	if failedSSE.Status != "failed" || failedSSE.ErrorCode != briefing.CodeGenerationFailed ||
		!strings.Contains(failedSSE.ErrorMessage, "purpose [admission]: admission failed") ||
		!strings.Contains(failedSSE.ErrorMessage, "change [admission]: admission failed") {
		t.Fatalf("live SSE snapshot lost admission cause: %+v", failedSSE)
	}

	// Fresh automatic read during cooldown returns the stored admission failure without calling the model.
	storedSnap := postBriefing(t, router, proj.ID, target, "automatic")
	if storedSnap.Status != "failed" || storedSnap.ErrorMessage != failedSSE.ErrorMessage || lite.calls.Load() != 0 {
		t.Fatalf("stored snapshot mismatch: %+v", storedSnap)
	}

	releaseLane()

	// Next automatic attempt stores purpose via lite, fails change on lite (no coordinator fallback for overlay).
	target2 := twoSectionTarget{Key: "cmd-2", Purpose: "run-tests-2", Change: "fail-change"}
	_ = postBriefing(t, router, proj.ID, target2, "automatic")
	partialFail := <-hub.snapshots
	if partialFail.Status != "failed" || !strings.Contains(partialFail.ErrorMessage, "change [generation]: provider request failed") {
		t.Fatalf("expected change provider failure, got %+v", partialFail)
	}
	if coord.calls.Load() != 0 {
		t.Fatalf("automatic overlay unexpectedly used coordinator: %d", coord.calls.Load())
	}

	// Manual retry reuses stored purpose (0 extra calls for purpose) and uses coordinator fallback for change!
	lite.calls.Store(0)
	retryPending := postBriefing(t, router, proj.ID, target2, "manual")
	if retryPending.RegenerateOutcome != "started" {
		t.Fatalf("expected manual retry to start: %+v", retryPending)
	}
	completedSSE := <-hub.snapshots
	if completedSSE.Status != "complete" || completedSSE.Result == nil || len(completedSSE.Result.Summary) != 2 {
		t.Fatalf("manual retry did not complete both sections: %+v", completedSSE)
	}
	if lite.calls.Load() != 1 || coord.calls.Load() != 1 {
		t.Fatalf("expected 1 lite call + 1 coordinator fallback call for missing change section, got lite=%d coord=%d",
			lite.calls.Load(), coord.calls.Load())
	}
	if !completedSSE.Result.Summary[0].Reused || completedSSE.Result.Summary[0].Text != "Runs the test suite." {
		t.Fatalf("purpose section was not reused on manual retry: %+v", completedSSE.Result.Summary[0])
	}
	if completedSSE.Result.Summary[1].Text != "All checks passed cleanly." {
		t.Fatalf("change section did not use coordinator fallback: %+v", completedSSE.Result.Summary[1])
	}
}
