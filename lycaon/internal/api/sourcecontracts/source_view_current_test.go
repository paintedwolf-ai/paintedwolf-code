package sourcecontracts

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCurrentSourceViewPagesHugeFileAndSharesSnapshot(t *testing.T) {
	server := contractfixture.NewTestServer(t)
	root := t.TempDir()
	p := contractfixture.CreateProjectForTest(t, server, root)
	Text := strings.Repeat("bounded source line\n", 300000)
	testutil.FailErr(t, "write oversized source", os.WriteFile(filepath.Join(root, "large.txt"), []byte(Text), 0o600))
	view := contractfixture.ComparisonViewForTest(t, server, p.ID, wire.SourceComparisonSelector{Current: &wire.CurrentComparisonSource{Kind: "current", RootID: p.Roots[0].ID, Path: "large.txt"}}, "after")
	if view.State != "ready" || view.Extent.Rows != 300000 {
		t.Fatalf("current source state = %+v", view)
	}
	frame := contractfixture.ComparisonRowsForTest(t, server, p.ID, view, 299900)
	if len(frame.Rows) != 100 || frame.Rows[0].AfterLine != 299901 || frame.Rows[0].Text != "bounded source line\n" {
		t.Fatalf("distant frame = %+v", frame)
	}
	bytes := server.Sources.Views.SnapshotBytes()
	fork := contractfixture.ComparisonViewForTest(t, server, p.ID, wire.SourceComparisonSelector{Retained: &wire.RetainedComparisonSource{Kind: "retained", ViewID: view.ID, Comparison: "before"}}, "full")
	if fork.State != "ready" || server.Sources.Views.SnapshotBytes() != bytes {
		t.Fatal("fork duplicated the immutable file snapshot")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodDelete, sourceapi.SourceViewURL(p.ID, view.ID), nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("release original: %d %s", response.Code, response.Body.String())
	}
	if got := contractfixture.ComparisonRowsForTest(t, server, p.ID, fork, 0); len(got.Rows) == 0 {
		t.Fatal("releasing the original invalidated its fork")
	}
	presentation := contractfixture.PresentationForTest(t, server, p.ID, fork.ID, fork.IntentRevision)
	testutil.FailErr(t, "replace source revision", os.WriteFile(filepath.Join(root, "large.txt"), []byte("replacement\n"), 0o600))
	response = httptest.NewRecorder()
	server.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodGet, sourceapi.SourceViewURL(p.ID, fork.ID)+"/presentations/"+presentation.ID+"/rows", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("retained source was invalidated: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodDelete, sourceapi.SourceViewURL(p.ID, fork.ID), nil))
	if response.Code != http.StatusNoContent || server.Sources.Views.SnapshotBytes() != 0 {
		t.Fatalf("release changed source: status=%d bytes=%d", response.Code, server.Sources.Views.SnapshotBytes())
	}
}
