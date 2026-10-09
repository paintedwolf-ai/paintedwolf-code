package contractfixture

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func DetectFolder(t *testing.T, srv *hostapi.Server, path string) api.FolderDetect {
	t.Helper()
	req := NewAuthedRequest(http.MethodGet,
		"/v1/projects/detect?path="+url.QueryEscape(path), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("detect status=%d body=%s", w.Code, w.Body.String())
	}
	var out api.FolderDetect
	testutil.FailErr(t, "decode detect", json.NewDecoder(w.Body).Decode(&out))
	return out
}

type FixedScanCoordinator struct {
	ErrScanCoordinator
	Scan api.CodeScan
}

func (f *FixedScanCoordinator) Get(context.Context, string) (*api.CodeScan, error) {
	out := f.Scan
	return &out, nil
}

func (f *FixedScanCoordinator) Summary(context.Context, string) (*api.CodeScan, error) {
	return scan.ApplyScanView(&f.Scan, "summary"), nil
}

func (c *FixedScanCoordinator) SnapshotStore() *sourcesnapshot.Store { return nil }

func NewDetectionsTestServer(t *testing.T) (*hostapi.Server, string) {
	t.Helper()
	cfgDir := t.TempDir()
	var last *detectionpack.Matcher
	rewire := func(m *detectionpack.Matcher) { last = m }
	srv, _, _ := NewSettingsTestServer(t, func(d *hostapi.Dependencies) {
		d.Storage.DataDir = cfgDir
		d.Scans.PublishDetections = rewire
	})
	root := configlayout.FindModuleRoot()
	boot := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: func() []extpacks.PackContent {
			content, err := extpacks.DiscoverStockContent()
			testutil.FailErr(t, "DiscoverStockContent", err)
			return content
		}(),
		Desired: extpacks.EmptyDesired(),
	})
	srv.Admin.SessionAdmin.Lifecycle.Sessions.SetEffectiveCatalogDeps(root, boot, nil)
	srv.Admin.SessionAdmin.Lifecycle.Sessions.Catalog().SetCatalogViewCache(catalogview.NewCache(root, slog.Default()))
	_ = last
	return srv, cfgDir
}

func WriteExtensionSuggestion(t *testing.T, root string) {
	t.Helper()
	WriteOverlay(t, root, extpacks.DeviceDesiredName, `format: 1
suggest:
  - id: acme/reviewed
    source: https://example.com/acme/reviewed.git
`)
}
