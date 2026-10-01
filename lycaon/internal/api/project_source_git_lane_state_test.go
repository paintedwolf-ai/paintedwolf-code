package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// stubFileHistory overrides one lineage query.
type stubFileHistory struct {
	git.GitManager
	err error
}

func (s stubFileHistory) FileHistory(
	context.Context, string, git.GitFileHistoryOpts,
) ([]git.GitFileCommit, error) {
	return nil, s.err
}

// Empty results retain their outcome state.
func TestSourceVersionsGitLaneNamesWhyItCouldNotAnswer(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want wire.SourceGitHistoryState
	}{
		{
			name: "walk still running when its budget elapsed",
			err:  git.ErrFileHistoryTimeout,
			want: wire.SourceGitHistoryStateTimedOut,
		},
		{
			name: "repository answered with an error",
			err:  errors.New("fatal: bad object HEAD"),
			want: wire.SourceGitHistoryStateFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMergedHistoryFixture(t)
			f.srv.board.Git = stubFileHistory{GitManager: f.srv.board.Git, err: tc.err}

			out := f.listing(t)

			if out.GitHistoryState != tc.want {
				t.Fatalf("git_history_state = %q, want %q", out.GitHistoryState, tc.want)
			}
			if out.GitHistoryState == wire.SourceGitHistoryStateAvailable {
				t.Fatal("a lane that never answered claimed to be available")
			}
			if len(out.Commits) != 0 || out.NextCursor != "" {
				t.Fatalf("commits=%d next=%q, want an empty unpaged lane",
					len(out.Commits), out.NextCursor)
			}
			// Retain versions when repository history fails.
			if len(out.Versions) == 0 {
				t.Fatal("git failure emptied the retained version lane")
			}
		})
	}
}

func TestSourceVersionsGitLaneReportsAbsenceSeparatelyFromFailure(t *testing.T) {
	f := newMergedHistoryFixture(t)
	repo := filepath.Dir(filepath.Dir(f.nested))
	testutil.FailErr(t, "detach repository", os.Rename(filepath.Join(repo, ".git"), filepath.Join(t.TempDir(), "git")))

	out := f.listing(t)

	if out.GitHistoryState != wire.SourceGitHistoryStateNoRepository {
		t.Fatalf("git_history_state = %q, want no_repository", out.GitHistoryState)
	}
	if len(out.Commits) != 0 {
		t.Fatalf("commits = %+v, want none", out.Commits)
	}
}

func TestSourceVersionsGitLaneReportsAnUnaskedFileAsNotTracked(t *testing.T) {
	f := newMergedHistoryFixture(t)

	req := newAuthedRequest(http.MethodGet,
		"/v1/projects/"+f.projectID+"/source/versions?file_id=00000000-0000-4000-8000-000000000000",
		nil)
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.SourceFileVersionsResponse
	testutil.FailErr(t, "decode listing", json.Unmarshal(w.Body.Bytes(), &out))

	if out.GitHistoryState != wire.SourceGitHistoryStateNotTracked {
		t.Fatalf("git_history_state = %q, want not_tracked", out.GitHistoryState)
	}
}
