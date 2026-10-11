package hostcontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestExtensionsMetaPackDisableEnable(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner

	suite := t.TempDir()
	contractfixture.WriteAPIMetaSuite(t, suite, "acme/api-kit", "leaf-a", "leaf-b")
	extstatetest.Apply(t, owner, extstatetest.DeviceScope(),
		extensionstate.InstallMetaOp{Source: "path:" + suite})

	kitStatus := func(out wire.ExtensionsCatalogView) *wire.ExtensionMetaPackSummary {
		for i := range out.MetaPacks {
			if out.MetaPacks[i].ID == "acme/api-kit" {
				return &out.MetaPacks[i]
			}
		}
		return nil
	}

	f := false
	body, _ := json.Marshal(wire.UpdateExtensionMetaPackRequest{
		Enabled:          &f,
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
	})
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/extensions/meta-packs/"+url.PathEscape("acme/api-kit"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", w.Code, w.Body.String())
	}
	out := contractfixture.DecodeMutationView(t, w.Body)
	if kit := kitStatus(out); kit == nil || kit.Status != wire.ExtensionMetaPackInactive {
		t.Fatalf("after disable want inactive: %+v", kit)
	}

	body, _ = json.Marshal(wire.ExtensionRevisionRequest{
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
	})
	req = contractfixture.NewAuthedRequest(http.MethodPost, "/v1/extensions/meta-packs/"+url.PathEscape("acme/api-kit")+"/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", w.Code, w.Body.String())
	}
	out = contractfixture.DecodeMutationView(t, w.Body)
	if kit := kitStatus(out); kit == nil || kit.Status != wire.ExtensionMetaPackComplete {
		t.Fatalf("after enable want complete: %+v", kit)
	}
}

// Disabling the stock suite rejects because the platform pack is required.

func TestExtensionsStockMetaPackDisableRejected(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner

	f := false
	body, _ := json.Marshal(wire.UpdateExtensionMetaPackRequest{
		Enabled:          &f,
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
	})
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/extensions/meta-packs/"+url.PathEscape(extpacks.StockMetaPackID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("stock disable status=%d body=%s, want 409", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"code":"stock_pack_immutable"`)) {
		t.Fatalf("rejection should carry stock_pack_immutable: %s", w.Body.String())
	}
}

func TestDeviceMetaPackMutationKeepsProjectTrustEnabled(t *testing.T) {
	// Journal events refresh the project view.
	sqlDB := testdbfixture.Open(t, "journal.db")
	srv := contractfixture.TrustSettingsServer(t, func(d *hostapi.Dependencies) { d.Extensions.ExtensionJournal = extensionstate.NewSQLJournal(sqlDB) })
	root := t.TempDir()
	contractfixture.WriteOverlay(t, root, extpacks.DeviceDesiredName, "format: 1\npacks: []\ndisabled: []\nown: {}\n")
	owner := srv.Admin.Extensions.Mutations.Owner
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "CreateWithRoot", err)
	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO projects(id, name, last_opened_at, created_at) VALUES(?, 'P', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, p.ID)
	testutil.FailErr(t, "seed project row", err)

	applies := func() bool {
		got, getErr := srv.Sources.Workspace.ProjectRegistry.Get(t.Context(), p.ID)
		testutil.FailErr(t, "Get project", getErr)
		return srv.Admin.Project.Trust.Settings.TrustSurfaces.Applies(projectcontrib.SurfaceExtensionConfig, *got)
	}
	if !applies() {
		t.Fatal("project extension suggestions are not applying")
	}

	suite := t.TempDir()
	contractfixture.WriteAPIMetaSuite(t, suite, "acme/api-kit", "leaf-a")
	extstatetest.Apply(t, owner,
		extstatetest.DeviceScope(),
		extensionstate.InstallMetaOp{Source: "path:" + suite})
	if !applies() {
		t.Fatal("suite install suppressed project extension suggestions")
	}

	f := false
	body, err := json.Marshal(wire.UpdateExtensionMetaPackRequest{
		Enabled:          &f,
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
	})
	testutil.FailErr(t, "marshal request", err)
	req := contractfixture.NewAuthedRequest(http.MethodPatch,
		"/v1/extensions/meta-packs/"+url.PathEscape("acme/api-kit"),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !applies() {
		t.Fatal("suite mutation suppressed project extension suggestions")
	}
}

func TestExtensionsMetaPackInstallRemove(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner

	suite := t.TempDir()
	contractfixture.WriteAPIMetaLeaf(t, suite, "leaf-a", "acme/api-leaf-a")
	contractfixture.WriteAPIMetaLeaf(t, suite, "leaf-b", "acme/api-leaf-b")
	testutil.FailErr(t, "meta.yaml", os.WriteFile(filepath.Join(suite, "meta.yaml"), []byte(
		"manifest_version: 1\nid: acme/api-kit\nname: API Kit\nversion: \"1.0.0\"\n"+
			"compatibility:\n  extension_api: \"^1.0.0\"\nmembers:\n  - acme/api-leaf-a\n  - acme/api-leaf-b\nconflicts_with: []\nextends: []\n",
	), 0o644))

	body, _ := json.Marshal(wire.ExtensionMetaPackInstallRequest{
		Source:           "path:" + suite,
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
	})
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/extensions/meta-packs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("install status=%d body=%s", w.Code, w.Body.String())
	}
	out := contractfixture.DecodeMutationView(t, w.Body)
	found := false
	for _, m := range out.MetaPacks {
		if m.ID == "acme/api-kit" {
			found = true
			if !m.Removable {
				t.Fatal("community meta should be removable")
			}
			if m.Members == nil || m.ConflictsWith == nil || m.Extends == nil || m.Diagnostics == nil {
				t.Fatalf("meta-pack arrays must be present: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("acme/api-kit missing after install")
	}

	rev := contractfixture.ExtensionRevision(t, owner, "")
	req = contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/extensions/meta-packs/"+url.PathEscape(extpacks.StockMetaPackID)+"?expected_revision="+url.QueryEscape(rev), nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("remove stock should be conflict, status=%d body=%s", w.Code, w.Body.String())
	}

	rev = contractfixture.ExtensionRevision(t, owner, "")
	req = contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/extensions/meta-packs/"+url.PathEscape("acme/api-kit")+"?expected_revision="+url.QueryEscape(rev), nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", w.Code, w.Body.String())
	}

	assertMetaPackRemoved := func(out wire.ExtensionsCatalogView, when string) {
		t.Helper()
		for _, m := range out.MetaPacks {
			if m.ID == "acme/api-kit" {
				t.Fatalf("%s: acme/api-kit still listed after remove: %+v", when, m)
			}
		}
		for _, p := range out.Packs {
			if p.ID == "acme/api-leaf-a" && slices.Contains(p.MetaPackIDs, "acme/api-kit") {
				t.Fatalf("%s: acme/api-leaf-a still carries removed meta_pack_id: %+v", when, p)
			}
		}
	}

	req = contractfixture.NewAuthedRequest(http.MethodGet, "/v1/extensions", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", w.Code, w.Body.String())
	}
	var getOut wire.ExtensionsCatalogView
	testutil.FailErr(t, "decode catalog", json.NewDecoder(w.Body).Decode(&getOut))
	assertMetaPackRemoved(getOut, "subsequent get")
}

func TestExtensionsMetaPackInstallAmbiguous(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	srv := contractfixture.NewExtensionsServer(t)
	owner := srv.Admin.Extensions.Mutations.Owner

	suite := t.TempDir()
	testutil.FailErr(t, "extension.yaml", os.WriteFile(filepath.Join(suite, "extension.yaml"), []byte(
		"manifest_version: 1\nid: acme/both\nname: Both\nversion: 1.0.0\ncompatibility:\n  extension_api: \"^1.0.0\"\n",
	), 0o644))
	testutil.FailErr(t, "meta.yaml", os.WriteFile(filepath.Join(suite, "meta.yaml"), []byte(
		"manifest_version: 1\nid: acme/both-meta\nname: Both\nversion: \"1.0.0\"\n"+
			"compatibility:\n  extension_api: \"^1.0.0\"\nmembers:\n  - acme/x\nconflicts_with: []\nextends: []\n",
	), 0o644))

	body, _ := json.Marshal(wire.ExtensionMetaPackInstallRequest{
		Source:           "path:" + suite,
		ExpectedRevision: contractfixture.ExtensionRevision(t, owner, ""),
	})
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/extensions/meta-packs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous want 400, got %d body=%s", w.Code, w.Body.String())
	}
}
