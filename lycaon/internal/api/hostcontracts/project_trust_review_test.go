package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestTrustReviewCapturesBeforeClearingAndRetainsDeletedText(t *testing.T) {
	registry := project.NewSQLRegistry(testdbfixture.Open(t, "store.db"))
	srv, _, _ := contractfixture.NewSettingsTestServer(t, func(d *hostapi.Dependencies) { d.Core.Projects = registry })
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	testutil.FailErr(t, "write initial instructions", os.WriteFile(path, []byte("first\n"), 0o600))
	p, err := project.CreateWithRoot(t.Context(), registry, root)
	testutil.FailErr(t, "create project", err)
	url := "/v1/projects/" + p.ID + "/trust"
	read := func() wire.ProjectTrust {
		t.Helper()
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodGet, url, nil))
		if response.Code != 200 {
			t.Fatalf("read trust: %d %s", response.Code, response.Body.String())
		}
		var result wire.ProjectTrust
		testutil.FailErr(t, "decode trust", json.Unmarshal(response.Body.Bytes(), &result))
		return result
	}
	Open := func() wire.ProjectTrustReview {
		t.Helper()
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, url+"/review", nil))
		if response.Code != 200 {
			t.Fatalf("open review: %d %s", response.Code, response.Body.String())
		}
		var result wire.ProjectTrust
		testutil.FailErr(t, "decode review", json.Unmarshal(response.Body.Bytes(), &result))
		if result.UnreadCount != 0 {
			t.Fatalf("opening did not clear indicator: %+v", result)
		}
		return result.Review
	}
	if read().UnreadCount != 1 {
		t.Fatal("initial file was not unread")
	}
	pending := read().Review
	first := Open()
	if !reflect.DeepEqual(first, pending) || !reflect.DeepEqual(first, read().Review) || !reflect.DeepEqual(first, Open()) {
		t.Fatal("opening changed the shared comparison")
	}
	if len(first.Changes) != 1 || first.Changes[0].Kind != "added" || first.Changes[0].After != "first\n" {
		t.Fatalf("initial review = %+v", first)
	}
	if read().UnreadCount != 0 {
		t.Fatal("opening left file unread")
	}
	testutil.FailErr(t, "edit instructions", os.WriteFile(path, []byte("second\n"), 0o600))
	if read().UnreadCount != 1 {
		t.Fatal("later edit did not light indicator")
	}
	second := Open()
	if len(second.Changes) != 1 || second.Changes[0].Before != "first\n" || second.Changes[0].After != "second\n" || second.Changes[0].Kind != "modified" {
		t.Fatalf("edit review = %+v", second)
	}
	if first.Changes[0].After != "first\n" {
		t.Fatal("earlier review changed with the file")
	}
	testutil.FailErr(t, "delete instructions", os.Remove(path))
	if got := read(); got.UnreadCount != 1 || len(got.Review.Changes) != 1 {
		t.Fatalf("last removal disappeared: %+v", got)
	}
	removed := Open()
	if len(removed.Changes) != 1 || removed.Changes[0].Kind != "removed" || removed.Changes[0].Before != "second\n" || removed.Changes[0].After != "" {
		t.Fatalf("removal review = %+v", removed)
	}
	if !reflect.DeepEqual(removed, Open()) || !reflect.DeepEqual(removed, read().Review) {
		t.Fatal("reopening replaced the retained removal comparison")
	}
}

func TestTrustReviewFailedCapturePreservesBaseline(t *testing.T) {
	srv, _, registry := contractfixture.NewSettingsTestServer(t)
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	testutil.FailErr(t, "write instructions", os.WriteFile(path, []byte("read me"), 0o600))
	p, err := project.CreateWithRoot(t.Context(), registry, root)
	testutil.FailErr(t, "create project", err)
	url := "/v1/projects/" + p.ID + "/trust/review"
	Open := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, url, nil))
		return response
	}
	if got := Open(); got.Code != 200 {
		t.Fatalf("initial opening: %d %s", got.Code, got.Body.String())
	}
	testutil.FailErr(t, "write unreadable encoding", os.WriteFile(path, []byte{0xff, 0xfe}, 0o600))
	if got := Open(); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid capture: %d %s", got.Code, got.Body.String())
	}
	testutil.FailErr(t, "write repaired instructions", os.WriteFile(path, []byte("repaired"), 0o600))
	got := Open()
	var review wire.ProjectTrust
	testutil.FailErr(t, "decode repaired review", json.Unmarshal(got.Body.Bytes(), &review))
	if len(review.Review.Changes) != 1 || review.Review.Changes[0].Before != "read me" {
		t.Fatalf("failed capture replaced baseline: %+v", review)
	}
}
