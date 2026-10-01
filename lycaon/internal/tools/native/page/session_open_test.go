package page

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestOpenHandlerIdempotentReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	workspace := t.TempDir()
	for site, body := range map[string]string{"site": `<h1>Hello</h1>`, "other": `<h1>Page 2</h1>`} {
		testutil.FailErr(t, "create "+site, os.MkdirAll(filepath.Join(workspace, site), 0o700))
		testutil.FailErr(t, "write "+site+" index", os.WriteFile(filepath.Join(workspace, site, "index.html"), []byte(body), 0o600))
	}
	pool := browser.NewPool("")
	pool.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	t.Cleanup(pool.Close)
	pages := pagesession.NewRegistry(pagesession.Config{MaxPages: 1})
	t.Cleanup(func() { pages.Close(t.Context()) })

	handler := OpenHandler(pool, pages, nil, nil)
	tctx := tools.ToolContext{
		SessionID: "sess-idempotent", ProjectID: "proj-1",
		Roots:        []projectroot.RootRef{{ID: "project", Path: workspace, IsPrimary: true}},
		ActiveRootID: "project",
	}

	// The first open holds a new page.
	raw1, err := handler(t.Context(), map[string]any{"project_dir": "site"}, tctx)
	testutil.FailErr(t, "first open", err)
	var res1 OpenResult
	testutil.FailErr(t, "decode res1", json.Unmarshal([]byte(raw1), &res1))
	if res1.ID == "" {
		t.Fatal("first open returned empty page ID")
	}
	if len(res1.LivePages) != 1 {
		t.Fatalf("expected 1 live page, got %d", len(res1.LivePages))
	}

	// Reopening the same target re-navigates the held page.
	raw2, err := handler(t.Context(), map[string]any{"project_dir": "site"}, tctx)
	testutil.FailErr(t, "second open", err)
	var res2 OpenResult
	testutil.FailErr(t, "decode res2", json.Unmarshal([]byte(raw2), &res2))
	if res2.ID != res1.ID {
		t.Fatalf("second open page ID = %q, want %q", res2.ID, res1.ID)
	}
	if len(res2.LivePages) != 1 {
		t.Fatalf("expected 1 live page after reopen, got %d", len(res2.LivePages))
	}

	// A distinct target on a registry capped at 1 is refused with PAGE_CAP_REACHED.
	_, err = handler(t.Context(), map[string]any{"project_dir": "other"}, tctx)
	var rejection *tools.ToolReject
	if !errors.As(err, &rejection) || rejection.Code != "PAGE_CAP_REACHED" {
		t.Fatalf("expected PAGE_CAP_REACHED for distinct target, got: %v", err)
	}
}
