package contract

import (
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubSourceGitReviewRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	beforeCommit, afterCommit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	mux.HandleFunc("GET /v1/projects/{id}/source/git-changes/{git_change_id}/review", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceGitReview{
			Change: api.SourceGitChange{
				ID: r.PathValue("git_change_id"), RootID: fixtureRootID, Kind: api.SourceGitChangeCommit,
				FromCommit: &beforeCommit, ToCommit: &afterCommit, Ordinal: 1, ObservedAt: fixtureTimeValue(),
			},
			Commit: api.SourceGitCommitDetails{
				Hash: afterCommit, Parents: []string{beforeCommit}, Message: "Update fixture", AuthorName: "Fixture",
				AuthoredAt: fixtureTimeValue(), CommittedAt: fixtureTimeValue(),
			},
			CommitComparison: true, BeforeCommit: beforeCommit, AfterCommit: afterCommit,
			Files: []api.SourceGitReviewFile{{
				Path: "fixture.ts", BeforePath: "fixture.ts", Op: api.SourceChangeOpWrite,
				BeforeMode: "100644", AfterMode: "100644", BeforeOid: strings.Repeat("c", 40), AfterOid: strings.Repeat("d", 40),
				Insertions: 1, Deletions: 1,
			}},
			FilesTotal: 1, Insertions: 1, Deletions: 1,
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/revisions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceRevisionsResponse{Comparisons: []api.SourceRevisionComparison{{
			RootID: fixtureRootID, Spec: r.URL.Query().Get("spec"), Kind: "commit", Label: afterCommit[:7] + " Update fixture",
			BeforeCommit: beforeCommit, AfterCommit: afterCommit, Subject: "Update fixture",
		}}})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/revisions/review", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceRevisionReview{
			RootID: r.URL.Query().Get("root_id"), BeforeCommit: beforeCommit, AfterCommit: afterCommit,
			Files: []api.SourceGitReviewFile{{
				Path: "fixture.ts", BeforePath: "fixture.ts", Op: api.SourceChangeOpWrite,
				BeforeMode: "100644", AfterMode: "100644", BeforeOid: strings.Repeat("c", 40), AfterOid: strings.Repeat("d", 40),
				Insertions: 1, Deletions: 1,
			}},
			FilesTotal: 1, Insertions: 1, Deletions: 1,
		})
	})
}
