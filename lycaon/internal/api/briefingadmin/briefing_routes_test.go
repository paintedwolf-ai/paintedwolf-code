package briefingadmin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/api/briefingadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefinghost"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// fixtureTarget is the fixture profile's wire target.
type fixtureTarget struct {
	Key      string `json:"key"`
	Text     string `json:"text"`
	Audience string `json:"audience"`
	Generate bool   `json:"generate"`
}

// fixtureProfile stands in for step, activity and worker profiles: it
// resolves its own target shape into one section.
type fixtureProfile struct{ policy briefing.Policy }

func (fixtureProfile) ID() string                { return "tool_activity" }
func (fixtureProfile) Version() string           { return "1" }
func (p fixtureProfile) Policy() briefing.Policy { return p.policy }

func (p fixtureProfile) Resolve(_ context.Context, target briefing.Target) (briefing.Request, error) {
	var ref fixtureTarget
	if err := json.Unmarshal(target.Ref, &ref); err != nil || ref.Key == "" {
		return briefing.Request{}, fmt.Errorf("%w: key is required", briefing.ErrInvalidTarget)
	}
	if ref.Key == "too-many" {
		return briefing.Request{}, briefing.ErrTargetUnavailable
	}
	manifest := briefing.Manifest{ProfileID: "tool_activity", ProfileVersion: "1", SectionID: "outcome", Template: briefing.Template{ID: "outcome", Version: "1"}, Contract: briefing.OneSentence,
		Items: []briefing.Item{{Key: "receipt", Role: briefing.RoleObservation, SourceKind: "receipt", Inclusion: briefing.IncludedWhole, Text: ref.Text, Audience: ref.Audience}}}
	section := briefing.SectionPlan{ID: "outcome", Mode: briefing.Deterministic, Manifest: manifest, Text: ref.Text}
	if ref.Generate {
		section.Mode, section.Prompt = briefing.Generated, briefing.Prompt{System: "s", User: ref.Text, MaxTokens: 40}
	}
	return briefing.Request{ProjectID: target.ProjectID, ProfileID: p.ID(), Version: p.Version(), TargetKey: ref.Key, TargetKind: "activity",
		TargetJSON: string(target.Ref), ScopeFingerprint: ref.Key, Manual: target.Manual, Policy: p.policy, Sections: []briefing.SectionPlan{section}}, nil
}

type engineRoutes struct {
	router  http.Handler
	project *project.Project
	host    *briefinghost.Host
}

// textGenerator stands in for a configured model.
type textGenerator struct{}

func (textGenerator) Generate(context.Context, briefing.Invocation) (string, string, string, error) {
	return "Ran the fixture.", "provider", "model", nil
}

func newEngineRoutes(t *testing.T, setting *switchable, policy briefing.Policy) engineRoutes {
	t.Helper()
	deps := apitest.Dependencies(t, api.Dependencies{})
	p, err := project.CreateWithRoot(t.Context(), deps.Core.Projects, t.TempDir())
	testutil.FailErr(t, "create project", err)
	database := testdbfixture.Open(t, "briefing-routes.db")
	testdbseed.InsertProjectRoot(t, database, p.ID, t.TempDir())
	host := briefinghost.New(context.Background(), database, t.TempDir(), textGenerator{})
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	testutil.FailErr(t, "register", host.Registry.Register(fixtureProfile{policy: policy}))
	files := filebriefing.NewService(context.Background(), filebriefing.Dependencies{Store: filebriefing.NewMemory(), Settings: setting})
	t.Cleanup(func() { files.Stop(); files.Wait(context.Background()) })
	h := briefingadmin.New(&httpio.Responder{Logger: slog.Default()}, briefingadmin.Dependencies{
		Briefings: briefingadmin.Services{Files: files, Host: host}, ProjectRegistry: deps.Core.Projects, SessionStore: deps.Core.Store,
	})
	r := chi.NewRouter()
	r.Post("/v1/projects/{id}/briefings", h.Engine.HandleRequestBriefing)
	r.Get("/v1/projects/{id}/briefings/records/{record_id}", h.Engine.HandleGetBriefingRecord)
	return engineRoutes{router: r, project: p, host: host}
}

func (e engineRoutes) request(t *testing.T, profileID string, target fixtureTarget, trigger string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"profile_id": profileID, "target_ref": target, "trigger": trigger})
	testutil.FailErr(t, "encode", err)
	req := httptest.NewRequest(http.MethodPost, "/v1/projects/"+e.project.ID+"/briefings", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func (e engineRoutes) record(t *testing.T, recordID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/projects/"+e.project.ID+"/briefings/records/"+recordID, nil))
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	testutil.FailErr(t, "decode "+w.Body.String(), json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func TestBriefingRequestServesSnapshotsAndRecords(t *testing.T) {
	routes := newEngineRoutes(t, &switchable{enabled: true}, briefing.QueuedPolicy)
	w := routes.request(t, "tool_activity", fixtureTarget{Key: "card", Text: "Exited 0 after 214 lines."}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	snap := decode[wire.BriefingSnapshot](t, w)
	if snap.Status != "idle" || snap.Result == nil || len(snap.Result.Summary) != 1 || snap.Result.Summary[0].Text != "Exited 0 after 214 lines." || snap.CacheEpoch != "1" || snap.Revision != "1" {
		t.Fatalf("snapshot %+v", snap)
	}
	if snap.Target["key"] != "card" || snap.Result.EvidenceRef.ProjectID != routes.project.ID {
		t.Fatalf("target or evidence lost: %+v", snap)
	}
	again := decode[wire.BriefingSnapshot](t, routes.request(t, "tool_activity", fixtureTarget{Key: "card", Text: "Exited 0 after 214 lines."}, "manual"))
	if again.RegenerateOutcome != "inputs_unchanged" || again.Result.EvidenceRef != snap.Result.EvidenceRef {
		t.Fatalf("an unchanged manual request did work: %+v", again)
	}
	record := decode[wire.BriefingRecord](t, routes.record(t, snap.Result.EvidenceRef.RecordID))
	if len(record.Sections) != 1 || !record.Sections[0].Available || record.Sections[0].Items[0].Text != "Exited 0 after 214 lines." || record.Sections[0].Result.Text == "" {
		t.Fatalf("record %+v", record)
	}
	if w := routes.record(t, strings.Repeat("0", 64)); w.Code != http.StatusNotFound {
		t.Fatalf("missing record: %d", w.Code)
	}
}

func TestBriefingRecordHidesSectionsTheReaderCannotRead(t *testing.T) {
	routes := newEngineRoutes(t, &switchable{enabled: true}, briefing.QueuedPolicy)
	snap := decode[wire.BriefingSnapshot](t, routes.request(t, "tool_activity", fixtureTarget{Key: "private", Text: "From another chat.", Audience: "00000000-0000-4000-8000-00000000dead"}, ""))
	record := decode[wire.BriefingRecord](t, routes.record(t, snap.Result.EvidenceRef.RecordID))
	if section := record.Sections[0]; section.Available || section.Result != nil || len(section.Items) != 0 {
		t.Fatalf("an unreadable section leaked: %+v", section)
	}
}

func TestBriefingRequestRefusals(t *testing.T) {
	routes := newEngineRoutes(t, &switchable{enabled: true}, briefing.QueuedPolicy)
	cases := []struct {
		name    string
		profile string
		target  fixtureTarget
		status  int
		code    wire.ApiErrorCode
	}{
		{"unregistered profile", "worker_result", fixtureTarget{Key: "k"}, http.StatusConflict, wire.ApiErrorCodeBriefingTargetUnavailable},
		{"malformed target", "tool_activity", fixtureTarget{}, http.StatusBadRequest, ""},
		{"over its bound", "tool_activity", fixtureTarget{Key: "too-many"}, http.StatusConflict, wire.ApiErrorCodeBriefingTargetUnavailable},
	}
	for _, c := range cases {
		w := routes.request(t, c.profile, c.target, "")
		if w.Code != c.status || (c.code != "" && !strings.Contains(w.Body.String(), string(c.code))) {
			t.Fatalf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	off := newEngineRoutes(t, &switchable{}, briefing.QueuedPolicy)
	if w := off.request(t, "tool_activity", fixtureTarget{Key: "k"}, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), string(wire.ApiErrorCodeBriefingDisabled)) {
		t.Fatalf("disabled: %d %s", w.Code, w.Body)
	}
}

func TestBriefingRequestReportsAFullQueue(t *testing.T) {
	full := briefing.QueuedPolicy
	full.QueueLimit = 0
	routes := newEngineRoutes(t, &switchable{enabled: true}, full)
	w := routes.request(t, "tool_activity", fixtureTarget{Key: "k", Text: "needs a model", Generate: true}, "")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), string(wire.ApiErrorCodeBriefingBusy)) {
		t.Fatalf("busy: %d %v %s", w.Code, w.Header(), w.Body)
	}
}
