package contract

import (
	"net/http"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubGitRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	gitStatus := api.GitStatusSummary{
		Available:     true,
		RepoID:        "rstubrepo0000001",
		RootIDs:       []string{"root-primary"},
		Branch:        "main",
		HeadShort:     "abc12345",
		Dirty:         true,
		StagedCount:   1,
		UnstagedCount: 1,
	}
	gitFiles := []api.GitFileEntry{
		{Path: "src/api/auth.go", Status: " M", RootID: "root-primary", RootRelativePath: "src/api/auth.go"},
		{Path: "src/ui/login.tsx", Status: "??", RootID: "root-primary", RootRelativePath: "src/ui/login.tsx"},
	}
	mux.HandleFunc("GET /v1/projects/{id}/git/repos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitReposView{
			ActiveRepoID: gitStatus.RepoID,
			Repos: []api.GitRepoEntry{{
				RepoID:        gitStatus.RepoID,
				Label:         "stub",
				RootIDs:       gitStatus.RootIDs,
				Available:     true,
				Branch:        gitStatus.Branch,
				HeadShort:     gitStatus.HeadShort,
				Dirty:         gitStatus.Dirty,
				StagedCount:   gitStatus.StagedCount,
				UnstagedCount: gitStatus.UnstagedCount,
			}},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/git/repos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.GitRepoEntry{
			RepoID:        gitStatus.RepoID,
			Label:         "stub",
			RootIDs:       gitStatus.RootIDs,
			Available:     true,
			Branch:        gitStatus.Branch,
			HeadShort:     gitStatus.HeadShort,
			Dirty:         gitStatus.Dirty,
			StagedCount:   gitStatus.StagedCount,
			UnstagedCount: gitStatus.UnstagedCount,
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/git/repos/{repo_id}/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitStatusSummary{
			Available: true, RepoID: gitStatus.RepoID, RootIDs: gitStatus.RootIDs,
			Revision: 1, Branch: gitStatus.Branch, HeadShort: gitStatus.HeadShort,
			Ahead: gitStatus.Ahead, Behind: gitStatus.Behind, Dirty: gitStatus.Dirty,
			StagedCount: gitStatus.StagedCount, UnstagedCount: gitStatus.UnstagedCount,
			ChangedCount: len(gitFiles),
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/git/repos/{repo_id}/changes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitChangesPage{
			RepoID: gitStatus.RepoID, Revision: 1, Files: gitFiles,
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/git/repos/{repo_id}/branches", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitBranchesView{
			Branches: []api.GitBranchEntry{
				{Name: "main", Current: true},
				{Name: "feat/login", Current: false},
			},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/git/repos/{repo_id}/checkout", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitMutationResult{RepoID: gitStatus.RepoID, Revision: 1, Refreshing: true})
	})
	mux.HandleFunc("POST /v1/projects/{id}/git/repos/{repo_id}/discard", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitMutationResult{RepoID: gitStatus.RepoID, Revision: 1, Refreshing: true})
	})
	mux.HandleFunc("POST /v1/projects/{id}/git/repos/{repo_id}/push", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitMutationResult{RepoID: gitStatus.RepoID, Revision: 1, Refreshing: true})
	})
	mux.HandleFunc("POST /v1/projects/{id}/git/repos/{repo_id}/pull", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitMutationResult{RepoID: gitStatus.RepoID, Revision: 1, Refreshing: true})
	})
	mux.HandleFunc("POST /v1/projects/{id}/git/repos/{repo_id}/commit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitMutationResult{RepoID: gitStatus.RepoID, Revision: 1, Refreshing: true})
	})
	mux.HandleFunc("POST /v1/projects/{id}/git/repos/{repo_id}/stash", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitMutationResult{RepoID: gitStatus.RepoID, Revision: 1, Refreshing: true})
	})
	mux.HandleFunc("POST /v1/projects/{id}/git/repos/{repo_id}/commit-message", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitCommitMessageResponse{Message: "feat(api): add auth check"})
	})
	unboundWorktree := api.GitWorktreeView{Bound: false, SessionID: "sess-stub"}
	mux.HandleFunc("GET /v1/sessions/{id}/git-worktree", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, unboundWorktree)
	})
	mux.HandleFunc("PUT /v1/sessions/{id}/git-worktree", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitWorktreeView{
			Bound: true, SessionID: "sess-stub", RepoID: gitStatus.RepoID, Label: "stub",
			Branch: "session/sess-stub", BaseBranch: "main", State: "ready",
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/git-worktree/land", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.GitWorktreeLandResult{
			Landed: true, BaseBranch: "main", Commits: 1, Reason: "merged", Conflicts: []string{},
		})
	})
	mux.HandleFunc("DELETE /v1/sessions/{id}/git-worktree", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/debug/den-perf", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}
