package contractfixture

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func RecordGitReviewMovement(t *testing.T, database db.Handle, p wire.Project, id, kind, before, after string, ordinal int) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `INSERT INTO source_git_transitions
		(id, project_id, root_id, kind, from_commit, to_commit, ordinal, observed_ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, '2026-09-10T10:00:00Z')`, id, p.ID, p.Roots[0].ID, kind, before, after, ordinal)
	testutil.FailErr(t, "record Git movement", err)
}

func RequestGitReview(t *testing.T, srv *hostapi.Server, projectID, id, suffix string, q url.Values, target any) int {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, NewAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/source/git-changes/"+id+"/"+suffix+"?"+q.Encode(), nil))
	if target != nil && w.Code == http.StatusOK {
		testutil.FailErr(t, "decode Git review", json.Unmarshal(w.Body.Bytes(), target))
	}
	return w.Code
}

func RequestSourceRevisions(t *testing.T, srv *hostapi.Server, projectID, suffix string, q url.Values, target any) int {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, NewAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/source/revisions"+suffix+"?"+q.Encode(), nil))
	if target != nil && w.Code == http.StatusOK {
		testutil.FailErr(t, "decode revisions", json.Unmarshal(w.Body.Bytes(), target))
	}
	return w.Code
}

type StubFileHistory struct {
	git.GitManager
	Err error
}

func (s StubFileHistory) FileHistory(
	context.Context, string, git.GitFileHistoryOpts,
) ([]git.GitFileCommit, error) {
	return nil, s.Err
}

// Empty results retain their outcome state.
