package extensionadmin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProviderContributionRefusesUnavailableOrMutableTools(t *testing.T) {
	c := &Contributions{responses: &httpio.Responder{Logger: slog.New(slog.DiscardHandler)}}
	cases := []struct {
		name, requirement, tool string
		ready                   bool
	}{
		{"invalid requirement", "invalid", "search_issues", true},
		{"unknown requirement", "acme/reviewer:missing", "search_issues", true},
		{"not ready", "acme/reviewer:tracker", "search_issues", false},
		{"unknown tool", "acme/reviewer:tracker", "missing", true},
		{"mutable tool", "acme/reviewer:tracker", "search_issues", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := contributionTestFrame(t, "catalog", "mcp", tc.ready)
			rec := httptest.NewRecorder()
			requirement, provider, ok := c.runnableContributionTool(rec, frame, tc.requirement, tc.tool)
			if ok || requirement != nil || provider != "" {
				t.Fatal("unavailable source admitted provider call")
			}
			assertContributionRefusal(t, rec, wire.ApiErrorCodeContributionSourceUnavailable)
		})
	}
}

func TestContributionAndPackMutationsRefuseMissingAdmissionCoordinates(t *testing.T) {
	responses := &httpio.Responder{Logger: slog.New(slog.DiscardHandler)}
	c := &Contributions{responses: responses}
	m := &Mutations{responses: responses}
	catalog := &Catalog{responses: responses}
	execution := &Execution{responses: responses}
	cases := []struct {
		name   string
		handle http.HandlerFunc
	}{
		{"pack profile", m.HandleApplyExtensionPackProfile}, {"pack update", m.HandleGetExtensionPackUpdate},
		{"apply pack update", m.HandleApplyExtensionPackUpdate}, {"reload pack", m.HandleReloadExtensionPack},
		{"meta update", catalog.HandleUpdateExtensionMetaPack}, {"meta delete", catalog.HandleDeleteExtensionMetaPack},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handle(rec, httptest.NewRequest(http.MethodPost, "/v1/extensions", nil))
			assertContributionRefusal(t, rec, wire.ApiErrorCodeInvalidRequest)
		})
	}
	rec := httptest.NewRecorder()
	invoke := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader("{}"))
	invoke.Header.Set("Content-Type", "application/json")
	execution.invokeCommand(rec, invoke, invokeScope{})
	assertContributionRefusal(t, rec, wire.ApiErrorCodeInvalidRequest)
	rec = httptest.NewRecorder()
	if frame, ok := c.contributionSourceFrame(rec, httptest.NewRequest(http.MethodPost, "/source", nil), nil, ""); ok || frame != nil {
		t.Fatal("missing frame revision captured a provider generation")
	}
	assertContributionRefusal(t, rec, wire.ApiErrorCodeInvalidRequest)
	rec = httptest.NewRecorder()
	if c.requireFrameRevision(rec, contributionTestFrame(t, "catalog", "mcp", true), "stale") {
		t.Fatal("stale generation admitted provider call")
	}
	assertContributionRefusal(t, rec, wire.ApiErrorCodeContributionFrameChanged)
	rec = httptest.NewRecorder()
	if m.checkUnitOwnChoice(rec, httptest.NewRequest(http.MethodPost, "/units", nil), wire.ExtensionsScopeProject, "unit", "pack") {
		t.Fatal("project applied device-only unit choice")
	}
	if rec.Code < 400 {
		t.Fatalf("missing project refusal: %d", rec.Code)
	}
}

func assertContributionRefusal(t *testing.T, rec *httptest.ResponseRecorder, want wire.ApiErrorCode) {
	t.Helper()
	var out wire.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		testutil.FailErr(t, "decode contribution refusal", err)
	}
	if out.Code != want || rec.Code != want.HTTPStatus() {
		t.Fatalf("status=%d code=%s want=%s", rec.Code, out.Code, want)
	}
}
