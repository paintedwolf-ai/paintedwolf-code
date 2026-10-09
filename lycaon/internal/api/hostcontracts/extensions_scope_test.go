package hostcontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestExtensionsUnitDisableProjectScope(t *testing.T) {
	srv, reg := contractfixture.ExtensionsScopeServer(t)
	projectDir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, projectDir)
	testutil.FailErr(t, "create project", err)

	disabled := false
	body, _ := json.Marshal(wire.UpdateExtensionUnitRequest{
		Enabled:          &disabled,
		ExpectedRevision: contractfixture.ExtensionRevision(t, extstatetest.Owner(t), projectDir),
	})
	req := contractfixture.NewAuthedRequest(http.MethodPatch,
		"/v1/extensions/units/"+url.PathEscape("workflows/bugbash")+"?scope=project&project_id="+p.ID,
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("project disable status=%d body=%s", w.Code, w.Body.String())
	}

	for _, endpoint := range []string{
		"/v1/extensions?project_id=" + p.ID,
		"/v1/extensions/units/" + url.PathEscape("workflows/bugbash") + "?project_id=" + p.ID,
	} {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodGet, endpoint, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("disabled unit response status=%d body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(endpoint, "/units/") {
			var detail wire.ExtensionUnitDetail
			testutil.FailErr(t, "decode disabled unit", json.Unmarshal(response.Body.Bytes(), &detail))
			if detail.Status != wire.ExtensionUnitStatus("disabled") || detail.Contributions == nil {
				t.Fatalf("disabled detail violates array contract: %+v", detail)
			}
		} else {
			var catalog wire.ExtensionsCatalogView
			testutil.FailErr(t, "decode disabled catalog", json.Unmarshal(response.Body.Bytes(), &catalog))
			for _, unit := range catalog.Units {
				if unit.Contributions == nil {
					t.Fatalf("unit %s has null contributions", unit.ID)
				}
			}
		}
	}

	data, err := os.ReadFile(filepath.Join(projectDir, settingsoverlay.DirName(), "extensions.yaml"))
	testutil.FailErr(t, "read project desired", err)
	if !bytes.Contains(data, []byte("workflows/bugbash")) {
		t.Fatalf("project desired missing unit: %s", data)
	}
	devicePath, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device path", err)
	if deviceData, err := os.ReadFile(devicePath); err == nil &&
		bytes.Contains(deviceData, []byte("workflows/bugbash")) {
		t.Fatalf("project write leaked into device desired: %s", deviceData)
	}
}

func TestExtensionsProjectScopeRequiresProjectID(t *testing.T) {
	srv, _ := contractfixture.ExtensionsScopeServer(t)
	disabled := false
	body, _ := json.Marshal(wire.UpdateExtensionUnitRequest{Enabled: &disabled})
	req := contractfixture.NewAuthedRequest(http.MethodPatch,
		"/v1/extensions/units/"+url.PathEscape("workflows/bugbash")+"?scope=project",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

func TestExtensionsProjectDisableRejectsDeviceUnit(t *testing.T) {
	srv, reg := contractfixture.ExtensionsScopeServer(t)
	projectDir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, projectDir)
	testutil.FailErr(t, "create project", err)
	before := contractfixture.ExtensionRevision(t, extstatetest.Owner(t), projectDir)
	disabled := false
	body, err := json.Marshal(wire.UpdateExtensionUnitRequest{Enabled: &disabled, ExpectedRevision: before})
	testutil.FailErr(t, "encode forbidden project disable", err)
	req := contractfixture.NewAuthedRequest(http.MethodPatch,
		"/v1/extensions/units/"+url.PathEscape("tools/schemas/write")+"?scope=project&project_id="+p.ID,
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("device unit project disable status=%d", w.Code)
	}
	if after := contractfixture.ExtensionRevision(t, extstatetest.Owner(t), projectDir); after != before {
		t.Fatal("rejected project disable changed extension state")
	}
	if _, err := os.Stat(extpacks.ProjectDesiredPath(projectDir)); !os.IsNotExist(err) {
		t.Fatalf("rejected project disable wrote a file: %v", err)
	}
}

func TestExtensionsMutationsStayOpenWhenSuggestionsOff(t *testing.T) {
	srv, _ := contractfixture.ExtensionsScopeServer(t)
	testutil.FailErr(t, "disable suggestions", srv.Admin.Project.Trust.Settings.TrustSurfaces.PutEnabled(map[string]bool{
		projectcontrib.SurfaceExtensionSuggestions: false,
	}))

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/extensions", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("catalog with suggestions off status=%d, want 200", w.Code)
	}

	body := `{"enabled":false,"expected_revision":"not-a-revision"}`
	patch := contractfixture.NewAuthedRequest(http.MethodPatch,
		"/v1/extensions/packs/"+url.PathEscape("painted-wolf/plan"),
		bytes.NewReader([]byte(body)))
	patch.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, patch)
	if resp.Code == http.StatusServiceUnavailable {
		t.Fatalf("suggestion switch off refused a catalog mutation: %s", resp.Body.String())
	}
}

func TestExtensionSuggestionAcceptRequiresTrust(t *testing.T) {
	srv, reg := contractfixture.ExtensionsScopeServer(t)
	p, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "create project", err)
	_, err = reg.SetTrustEnabled(t.Context(), p.ID, map[string]bool{
		projectcontrib.SurfaceExtensionSuggestions: false,
	})
	testutil.FailErr(t, "disable suggestions", err)

	req := contractfixture.NewAuthedRequest(http.MethodPost,
		"/v1/extensions/suggestions/accept?project_id="+url.QueryEscape(p.ID),
		bytes.NewReader([]byte(`{"pack_ids":["acme/reviewed"]}`)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "trust_surface_off") {
		t.Fatalf("status=%d body=%s, want trust refusal", w.Code, w.Body.String())
	}
}

func TestExtensionSuggestionAcceptIsBoundToReviewedContribution(t *testing.T) {
	srv, reg := contractfixture.ExtensionsScopeServer(t)
	projectDir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, projectDir)
	testutil.FailErr(t, "create project", err)
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "create overlay", os.MkdirAll(overlayDir, 0o755))
	proposalPath := extpacks.ProjectDesiredPath(projectDir)
	writeProposal := func(version string) {
		t.Helper()
		body := "format: 1\nsuggest:\n  - id: acme/reviewed\n    source: https://example.com/acme/reviewed.git\n    version: " + version + "\n"
		testutil.FailErr(t, "write proposal", os.WriteFile(proposalPath, []byte(body), 0o644))
	}
	writeProposal("^1.0.0")

	suggestionsReq := contractfixture.NewAuthedRequest(http.MethodGet,
		"/v1/extensions/suggestions?project_id="+url.QueryEscape(p.ID), nil)
	suggestionsW := httptest.NewRecorder()
	srv.ServeHTTP(suggestionsW, suggestionsReq)
	if suggestionsW.Code != http.StatusOK {
		t.Fatalf("suggestions status=%d body=%s", suggestionsW.Code, suggestionsW.Body.String())
	}
	var suggestions wire.ExtensionSuggestionsResponse
	testutil.FailErr(t, "decode suggestions", json.Unmarshal(suggestionsW.Body.Bytes(), &suggestions))
	if suggestions.SuggestionRevision == "" {
		t.Fatal("suggestions omitted revision")
	}

	writeProposal("^2.0.0")
	body, err := json.Marshal(wire.ExtensionSuggestionsAcceptRequest{
		PackIDs:                    []string{"acme/reviewed"},
		ExpectedRevision:           "not-reached-before-proposal-check",
		ExpectedSuggestionRevision: suggestions.SuggestionRevision,
	})
	testutil.FailErr(t, "encode accept", err)
	acceptReq := contractfixture.NewAuthedRequest(http.MethodPost,
		"/v1/extensions/suggestions/accept?project_id="+url.QueryEscape(p.ID), bytes.NewReader(body))
	acceptW := httptest.NewRecorder()
	srv.ServeHTTP(acceptW, acceptReq)
	if acceptW.Code != http.StatusConflict || !strings.Contains(acceptW.Body.String(), "extension_suggestion_changed") {
		t.Fatalf("accept status=%d body=%s, want reviewed-proposal conflict", acceptW.Code, acceptW.Body.String())
	}
}

func TestExtensionSuggestionAcceptValidatesWholeSelectionBeforeMutation(t *testing.T) {
	srv, reg := contractfixture.ExtensionsScopeServer(t)
	projectDir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, projectDir)
	testutil.FailErr(t, "create project", err)
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "create overlay", os.MkdirAll(overlayDir, 0o755))
	proposal := extpacks.SuggestionManifest{
		Format: extpacks.DesiredFormat,
		Suggest: []extpacks.SuggestedPack{{
			ID: "acme/reviewed", Source: "https://example.com/acme/reviewed.git",
		}},
	}
	encoded, err := extpacks.EncodeSuggestion(proposal)
	testutil.FailErr(t, "encode proposal", err)
	testutil.FailErr(t, "write proposal", os.WriteFile(extpacks.ProjectDesiredPath(projectDir), encoded, 0o644))

	before, err := srv.Admin.Extensions.Mutations.Owner.CurrentRevision("")
	testutil.FailErr(t, "read extension revision", err)
	body, err := json.Marshal(wire.ExtensionSuggestionsAcceptRequest{
		PackIDs:                    []string{"acme/reviewed", "acme/not-reviewed"},
		ExpectedRevision:           before,
		ExpectedSuggestionRevision: extpacks.SuggestionRevision(proposal),
	})
	testutil.FailErr(t, "encode accept", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost,
		"/v1/extensions/suggestions/accept?project_id="+url.QueryEscape(p.ID), bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s want whole-selection rejection", w.Code, w.Body.String())
	}
	after, err := srv.Admin.Extensions.Mutations.Owner.CurrentRevision("")
	testutil.FailErr(t, "read extension revision after rejection", err)
	if after != before {
		t.Fatalf("invalid reviewed selection partially mutated extensions: before=%s after=%s", before, after)
	}
}
