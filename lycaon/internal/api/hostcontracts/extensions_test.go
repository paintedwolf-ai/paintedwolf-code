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

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestExtensionsListAuthAndStock(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	srv := contractfixture.NewExtensionsServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/extensions", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed status=%d", w.Code)
	}

	req = contractfixture.NewAuthedRequest(http.MethodGet, "/v1/extensions", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.ExtensionsCatalogView
	testutil.FailErr(t, "decode", json.NewDecoder(w.Body).Decode(&out))
	if !out.OK {
		t.Fatalf("expected OK stock resolve, conflicts=%d", out.Conflicts)
	}
	if out.Revision == "" {
		t.Fatal("list must report the current extension revision")
	}
	found := false
	for _, p := range out.Packs {
		if p.ID == "painted-wolf/platform" && p.Contributing {
			found = true
			if p.Version == "" || p.ExtensionAPI == "" || p.InstallationState != "stock" || p.InstallationScope != "stock" {
				t.Fatalf("stock versioning summary=%+v", p)
			}
		}
	}
	if !found {
		t.Fatal("platform pack missing from list")
	}
	stockMeta := false
	for _, m := range out.MetaPacks {
		if m.ID == extpacks.StockMetaPackID {
			stockMeta = true
			if m.Status != wire.ExtensionMetaPackComplete {
				t.Fatalf("stock meta status=%s", m.Status)
			}
			if len(m.Members) == 0 {
				t.Fatal("stock meta pack must include members")
			}
			if m.Removable {
				t.Fatal("stock meta must not be removable")
			}
		}
	}
	if !stockMeta {
		t.Fatal("painted-wolf/stock missing from meta_packs")
	}
	for _, p := range out.Packs {
		if strings.HasPrefix(p.ID, "painted-wolf/") && len(p.MetaPackIDs) == 0 {
			t.Fatalf("pack %s missing meta_pack_ids", p.ID)
		}
	}
}

func TestExtensionsInstallRemoveAndStockRemoveRejected(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner

	fixture := filepath.Join(t.TempDir(), "pack")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(fixture, "policy"), 0o755))
	testutil.FailErr(t, "manifest", os.WriteFile(filepath.Join(fixture, "extension.yaml"), []byte(
		"manifest_version: 1\nid: acme/wire-pack\nname: wire\nversion: 1.0.0\n"+
			"compatibility:\n  extension_api: \"^1.0.0\"\ndependencies:\n  painted-wolf/platform:\n    version: \"^1.0.0\"\n",
	), 0o644))
	testutil.FailErr(t, "policy", os.WriteFile(filepath.Join(fixture, "policy", "WIRE_HELLO.yaml"), []byte(
		"id: WIRE_HELLO\nemit: banner\nmessage: hi\n",
	), 0o644))

	body, _ := json.Marshal(wire.ExtensionInstallRequest{
		Source:           "path:" + fixture,
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
	})
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/extensions/packs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("install status=%d body=%s", w.Code, w.Body.String())
	}
	var inst wire.ExtensionInstallResponse
	testutil.FailErr(t, "decode install", json.NewDecoder(w.Body).Decode(&inst))
	if inst.PackID != "acme/wire-pack" || inst.Version != "1.0.0" {
		t.Fatalf("install response=%+v", inst)
	}

	req = contractfixture.NewAuthedRequest(http.MethodGet, "/v1/extensions", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list installed status=%d body=%s", w.Code, w.Body.String())
	}
	rawBody := w.Body.Bytes()
	var installed wire.ExtensionsCatalogView
	testutil.FailErr(t, "decode installed list", json.Unmarshal(rawBody, &installed))
	foundInstalled := false
	for _, pack := range installed.Packs {
		if pack.ID != "acme/wire-pack" {
			continue
		}
		foundInstalled = true
		if pack.Version != "1.0.0" || pack.InstallationState != "development" ||
			pack.InstallationScope != "device" || !pack.Removable || pack.Dependencies["painted-wolf/platform"] != "1.0.0" {
			t.Fatalf("installed versioning summary=%+v", pack)
		}
	}
	// Raw JSON distinguishes an empty meta_pack_ids array from null or omission.
	var raw struct {
		Packs []struct {
			ID          string          `json:"id"`
			MetaPackIDs json.RawMessage `json:"meta_pack_ids"`
		} `json:"packs"`
	}
	testutil.FailErr(t, "decode raw installed list", json.Unmarshal(rawBody, &raw))
	rawFound := false
	for _, pack := range raw.Packs {
		if pack.ID != "acme/wire-pack" {
			continue
		}
		rawFound = true
		if string(pack.MetaPackIDs) != "[]" {
			t.Fatalf("acme/wire-pack meta_pack_ids raw=%s, want []", pack.MetaPackIDs)
		}
	}
	if !rawFound {
		t.Fatal("acme/wire-pack missing from raw packs")
	}
	if !foundInstalled {
		t.Fatal("installed package missing from list")
	}

	req = contractfixture.NewAuthedRequest(http.MethodDelete,
		"/v1/extensions/packs/"+url.PathEscape("painted-wolf/plan")+
			"?expected_revision="+url.QueryEscape(contractfixture.ExtensionRevision(t, owner, "")), nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("stock remove should be conflict, status=%d body=%s", w.Code, w.Body.String())
	}

	req = contractfixture.NewAuthedRequest(http.MethodDelete,
		"/v1/extensions/packs/"+url.PathEscape("acme/wire-pack")+
			"?expected_revision="+url.QueryEscape(contractfixture.ExtensionRevision(t, owner, "")), nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestExtensionsUnitConflictDetail(t *testing.T) {
	testutil.SkipIfShort(t, "installs conflicting packs and resolves the full device catalog")
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessionstore.NewMemory()}, Storage: hostapi.StorageDependencies{ModuleRoot: configlayout.FindModuleRoot()}}), nil, hostapi.TestAPIToken)

	dir := t.TempDir()
	for _, leaf := range []struct{ id, msg string }{
		{"acme/c1", "one"}, {"acme/c2", "two"},
	} {
		p := filepath.Join(dir, strings.ReplaceAll(leaf.id, "/", "_"))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(p, "policy"), 0o755))
		testutil.FailErr(t, "man", os.WriteFile(filepath.Join(p, "extension.yaml"), []byte(
			"manifest_version: 1\nid: "+leaf.id+"\nname: "+leaf.id+"\nversion: 1.0.0\n"+
				"compatibility:\n  extension_api: \"^1.0.0\"\ndependencies:\n  painted-wolf/platform:\n    version: \"^1.0.0\"\n",
		), 0o644))
		testutil.FailErr(t, "pol", os.WriteFile(filepath.Join(p, "policy", "WIRE_DUP.yaml"), []byte(
			"id: WIRE_DUP\nemit: banner\nmessage: "+leaf.msg+"\n",
		), 0o644))
		extstatetest.InstallPack(t, extstatetest.DeviceScope(), "path:"+p, "", "")
	}

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/extensions/units/"+url.PathEscape("policy/WIRE_DUP"), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unit status=%d body=%s", w.Code, w.Body.String())
	}
	var detail wire.ExtensionUnitDetail
	testutil.FailErr(t, "decode", json.NewDecoder(w.Body).Decode(&detail))
	if detail.Status != wire.ExtensionUnitStatusConflict {
		t.Fatalf("status=%s want conflict", detail.Status)
	}
	if len(detail.Contributions) < 2 {
		t.Fatalf("contributions=%d", len(detail.Contributions))
	}
}

func TestExtensionsUnitDisableDevice(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner

	disabled := false
	body, _ := json.Marshal(wire.UpdateExtensionUnitRequest{
		Enabled:          &disabled,
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
	})
	req := contractfixture.NewAuthedRequest(http.MethodPatch,
		"/v1/extensions/units/"+url.PathEscape("workflows/plan")+"?scope=device",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.ExtensionMutationResponse
	testutil.FailErr(t, "decode", json.NewDecoder(w.Body).Decode(&out))
	if out.View.Revision == "" {
		t.Fatal("mutation response must carry the committed revision")
	}
	found := false
	for _, id := range out.View.Desired.Disabled {
		if id == "workflows/plan" {
			found = true
		}
	}
	if !found {
		t.Fatalf("desired.disabled missing workflows/plan: %+v", out.View.Desired.Disabled)
	}
}

func TestExtensionsConfigurationCommitsEveryPackAtomically(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner
	body, err := json.Marshal(wire.ExtensionConfigurationRequest{
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
		Packs: map[string]map[string]any{
			"acme/reviewer": {"depth": "normal"},
			"other/alerts":  {"enabled": true},
		},
	})
	testutil.FailErr(t, "encode configuration", err)
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/extensions/configuration", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("configuration status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.ExtensionMutationResponse
	testutil.FailErr(t, "decode configuration", json.NewDecoder(w.Body).Decode(&out))
	if out.View.Desired.Configuration["acme/reviewer"]["depth"] != "normal" ||
		out.View.Desired.Configuration["other/alerts"]["enabled"] != true {
		t.Fatalf("configuration = %+v", out.View.Desired.Configuration)
	}
}

// TestExtensionsMutationStaleRevisionConflict maps optimistic staleness to 409.

func TestExtensionsMutationStaleRevisionConflict(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner

	stale := contractfixture.ExtensionRevision(t, owner, "")
	extstatetest.Apply(t, owner, extstatetest.DeviceScope(),
		extensionstate.SetUnitDisabledOp{UnitID: "workflows/plan", Disabled: true})

	disabled := false
	body, _ := json.Marshal(wire.UpdateExtensionUnitRequest{
		Enabled:          &disabled,
		ExpectedRevision: stale,
	})
	req := contractfixture.NewAuthedRequest(http.MethodPatch,
		"/v1/extensions/units/"+url.PathEscape("workflows/bugbash")+"?scope=device",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale mutation status=%d body=%s, want 409", w.Code, w.Body.String())
	}
	var errBody struct {
		Code string `json:"code"`
	}
	testutil.FailErr(t, "decode error", json.Unmarshal(w.Body.Bytes(), &errBody))
	if errBody.Code != "extension_state_changed" {
		t.Fatalf("code=%q, want extension_state_changed", errBody.Code)
	}
}

// TestExtensionsDeviceMutationRefreshesActiveCatalog checks process catalog refresh.

func TestExtensionsDeviceMutationRefreshesActiveCatalog(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	t.Cleanup(extpacks.ClearActive)

	boot, err := extpacks.ApplyCatalog(t.Context(), nil, nil)
	testutil.FailErr(t, "boot resolve", err)
	if !boot.PackContributed("painted-wolf/security") {
		t.Fatal("fixture: security pack must contribute before the mutation")
	}

	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner
	disabled := false
	body, _ := json.Marshal(wire.UpdateExtensionPackRequest{
		Enabled:          &disabled,
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
	})
	req := contractfixture.NewAuthedRequest(http.MethodPatch,
		"/v1/extensions/packs/"+url.PathEscape("painted-wolf/security"),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", w.Code, w.Body.String())
	}

	active := extpacks.Active()
	if active == nil {
		t.Fatal("Active catalog cleared by the mutation")
	}
	if active.PackContributed("painted-wolf/security") {
		t.Fatal("Active still contributes painted-wolf/security after a device disable")
	}
}

// TestExtensionsMutationsRequireFlagPresent rejects missing boolean values.

func TestExtensionsMutationsRequireFlagPresent(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	srv := contractfixture.NewExtensionsServer(t)

	cases := []struct{ name, path string }{
		{"pack enabled", "/v1/extensions/packs/" + url.PathEscape("painted-wolf/plan")},
		{"unit disabled", "/v1/extensions/units/" + url.PathEscape("workflows/plan") + "?scope=device"},
	}
	for _, tc := range cases {
		req := contractfixture.NewAuthedRequest(http.MethodPatch, tc.path, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: empty body status=%d body=%s, want 400", tc.name, w.Code, w.Body.String())
		}
	}
}

// TestExtensionsRemoveRejectsTraversalPackID rejects unsafe filesystem segments.

func TestExtensionsRemoveRejectsTraversalPackID(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	testutil.FailErr(t, "seed creds",
		os.WriteFile(filepath.Join(cfg, "credentials.json"), []byte("secret"), 0o600))
	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner

	for _, id := range []string{"%2E%2E", "%2E", "..", "."} {
		req := contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/extensions/packs/"+id+
			"?expected_revision="+url.QueryEscape(contractfixture.ExtensionRevision(t, owner, "")), nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code == http.StatusOK || w.Code == http.StatusNoContent {
			t.Errorf("DELETE pack_id=%s returned %d, want rejection", id, w.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg, "credentials.json")); err != nil {
		testutil.FailErr(t, "config dir survived", err)
	}
}

// One resolve answers every field, so a pack the view lists and a unit it counts
// cannot come from different generations.

func TestExtensionsCatalogViewIsOneGeneration(t *testing.T) {
	srv := contractfixture.NewExtensionsServer(t)

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/extensions", nil))
	var view wire.ExtensionsCatalogView
	testutil.FailErr(t, "decode", json.NewDecoder(w.Body).Decode(&view))

	if len(view.Packs) == 0 {
		t.Fatal("view must list the stock packs")
	}
	if len(view.Units) == 0 {
		t.Fatal("view must carry the units its packs resolved to")
	}
	if view.Revision == "" {
		t.Fatal("view must identify its generation")
	}
	if view.DesiredPath == "" {
		t.Fatal("view must name the intent file it resolved from")
	}
	counted := 0
	for _, u := range view.Units {
		if u.Status == wire.ExtensionUnitStatusConflict {
			counted++
		}
	}
	if counted != view.Conflicts {
		t.Fatalf("conflicts=%d but %d units report conflict — two resolves", view.Conflicts, counted)
	}
}
