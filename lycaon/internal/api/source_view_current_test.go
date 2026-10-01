package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCurrentSourceViewPagesHugeFileAndSharesSnapshot(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	p := createProjectForTest(t, server, root)
	text := strings.Repeat("bounded source line\n", 300000)
	testutil.FailErr(t, "write oversized source", os.WriteFile(filepath.Join(root, "large.txt"), []byte(text), 0o600))
	view := comparisonViewForTest(t, server, p.ID, wire.SourceComparisonSelector{Current: &wire.CurrentComparisonSource{Kind: "current", RootID: p.Roots[0].ID, Path: "large.txt"}}, "after")
	if view.State != "ready" || view.Extent.Rows != 300000 {
		t.Fatalf("current source state = %+v", view)
	}
	frame := comparisonRowsForTest(t, server, p.ID, view, 299900)
	if len(frame.Rows) != 100 || frame.Rows[0].AfterLine != 299901 || frame.Rows[0].Text != "bounded source line\n" {
		t.Fatalf("distant frame = %+v", frame)
	}
	bytes := server.Sources.SnapshotBytes()
	fork := comparisonViewForTest(t, server, p.ID, wire.SourceComparisonSelector{Retained: &wire.RetainedComparisonSource{Kind: "retained", ViewID: view.ID, Comparison: "before"}}, "full")
	if fork.State != "ready" || server.Sources.SnapshotBytes() != bytes {
		t.Fatal("fork duplicated the immutable file snapshot")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, newAuthedRequest(http.MethodDelete, sourceapi.SourceViewURL(p.ID, view.ID), nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("release original: %d %s", response.Code, response.Body.String())
	}
	if got := comparisonRowsForTest(t, server, p.ID, fork, 0); len(got.Rows) == 0 {
		t.Fatal("releasing the original invalidated its fork")
	}
	presentation := presentationForTest(t, server, p.ID, fork.ID, fork.IntentRevision)
	testutil.FailErr(t, "replace source revision", os.WriteFile(filepath.Join(root, "large.txt"), []byte("replacement\n"), 0o600))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, newAuthedRequest(http.MethodGet, sourceapi.SourceViewURL(p.ID, fork.ID)+"/presentations/"+presentation.ID+"/rows", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("retained source was invalidated: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, newAuthedRequest(http.MethodDelete, sourceapi.SourceViewURL(p.ID, fork.ID), nil))
	if response.Code != http.StatusNoContent || server.Sources.SnapshotBytes() != 0 {
		t.Fatalf("release changed source: status=%d bytes=%d", response.Code, server.Sources.SnapshotBytes())
	}
}
