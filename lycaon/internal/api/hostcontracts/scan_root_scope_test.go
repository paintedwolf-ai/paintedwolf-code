package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestScanRootSelectionUsesAttachedIdentity(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	p := &project.Project{Roots: []project.Root{
		{ID: "primary", Path: "/workspace/primary", IsPrimary: true},
		{ID: "secondary", Path: "/workspace/secondary"},
	}}
	for _, all := range []bool{false, true} {
		for _, id := range []string{"", "primary", "secondary", "foreign", "/workspace/secondary"} {
			t.Run(id+"/"+map[bool]string{false: "one", true: "all"}[all], func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/?root_id="+url.QueryEscape(id), nil)
				w := httptest.NewRecorder()
				got, ok := srv.Admin.Scan.RequireScanRoots(w, req, p, all)
				if id == "foreign" || id == "/workspace/secondary" {
					if ok || w.Code != http.StatusNotFound {
						t.Fatalf("unattached identity accepted: %v, %d", got, w.Code)
					}
					return
				}
				want := []string{"/workspace/primary"}
				if id == "secondary" {
					want = []string{"/workspace/secondary"}
				} else if id == "" && all {
					want = append(want, "/workspace/secondary")
				}
				if !ok || !reflect.DeepEqual(got, want) {
					t.Fatalf("root selection = %v, %v; want %v", got, ok, want)
				}
			})
		}
	}
}

func TestEveryScanRootRouteRejectsForeignMembership(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	base := "/v1/projects/" + proj.ID
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, base + "/scans?", ""},
		{http.MethodPost, base + "/scans?", `{}`},
		{http.MethodGet, base + "/security?", ""},
		{http.MethodPost, base + "/findings/query?", `{}`},
		{http.MethodGet, base + "/findings/ignores?", ""},
		{http.MethodPost, base + "/findings/ignores?", `{"path":"**","reason":"fixture"}`},
		{http.MethodDelete, base + "/findings/ignores/example?", ""},
		{http.MethodPost, base + "/findings/export?", `{"format":"sarif"}`},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			w := contractfixture.LedgerRequest(t, srv, route.method, route.path+"root_id=foreign", route.body)
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"root_not_found"`) {
				t.Fatalf("foreign scope status = %d, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSecondaryRootIgnoreDoesNotWritePrimaryRoot(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	secondary := t.TempDir()
	change, err := srv.Sources.Workspace.ProjectRegistry.AttachRoot(t.Context(), proj.ID, project.AttachRootParams{Path: secondary})
	testutil.FailErr(t, "attach secondary root", err)
	base := "/v1/projects/" + proj.ID + "/findings/ignores"
	w := contractfixture.LedgerRequest(t, srv, http.MethodPost, base+"?root_id="+change.Added.ID,
		`{"path":"fixture/**","kind":"sast","reason":"secondary fixture"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("secondary ignore status = %d, body=%s", w.Code, w.Body.String())
	}
	var secondaryList wire.FindingIgnoreListResponse
	testutil.FailErr(t, "decode secondary catalog", json.Unmarshal(w.Body.Bytes(), &secondaryList))
	if secondaryList.Path != filepath.Join(change.Added.Path, ".paintedwolf", "ignores.yaml") {
		t.Fatalf("ignore written outside selected root: %s", secondaryList.Path)
	}
	w = contractfixture.LedgerRequest(t, srv, http.MethodGet, base, "")
	var primaryList wire.FindingIgnoreListResponse
	testutil.FailErr(t, "decode primary catalog", json.Unmarshal(w.Body.Bytes(), &primaryList))
	for _, rule := range primaryList.Rules {
		if rule.Withdrawable || rule.Path == "fixture/**" {
			t.Fatalf("secondary decision leaked into primary catalog: %+v", rule)
		}
	}
}
