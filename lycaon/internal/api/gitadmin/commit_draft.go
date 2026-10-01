package gitadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HandleGitCommitMessage drafts a commit message from the current diff via the lite model.
func (s *Handler) HandleGitCommitMessage(w http.ResponseWriter, r *http.Request) {
	mgr := s.git
	repo, p, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	diff := combinedDiff(r.Context(), mgr, repo.Toplevel)
	if strings.TrimSpace(diff) == "" {
		s.responses.Fail(w, wire.ApiErrorCodeGitNoChanges, "no changes to summarize")
		return
	}
	ctx := curationctx.WithSession(r.Context(), curationctx.Session{
		SessionID:  strings.TrimSpace(r.URL.Query().Get("session_id")),
		ProjectID:  p.ID,
		ProjectDir: repo.Toplevel,
	})
	msg, err := s.draftCommitMessage(ctx, repo.Toplevel, diff)
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeGitDraftFailed, "could not draft a commit message")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.GitCommitMessageResponse{Message: msg})
}

// combinedDiff concatenates staged and unstaged hunks for the commit-message draftsman.
func combinedDiff(ctx context.Context, mgr git.GitManager, dir string) string {
	var b strings.Builder
	if staged, err := mgr.Diff(ctx, dir, git.GitDiffOpts{Staged: true}); err == nil {
		b.WriteString(staged)
	}
	if unstaged, err := mgr.Diff(ctx, dir, git.GitDiffOpts{}); err == nil {
		if b.Len() > 0 && unstaged != "" {
			b.WriteString("\n")
		}
		b.WriteString(unstaged)
	}
	if untracked, err := mgr.UntrackedDiff(ctx, dir); err == nil {
		if b.Len() > 0 && untracked != "" {
			b.WriteString("\n")
		}
		b.WriteString(untracked)
	}
	return b.String()
}

// errEmptyDraft reports a lite model that answered with nothing usable.
var errEmptyDraft = errors.New("lite model returned an empty draft")

// draftCommitMessage summarizes a diff into a single subject line via the lite model.
func (s *Handler) draftCommitMessage(ctx context.Context, dir, diff string) (string, error) {
	summarizer := s.CommitDrafter
	if summarizer == nil {
		if !llm.ProviderUtilityCallsEnabled() {
			return "", compaction.ErrNoLiteProvider
		}
		summarizer = s.LLMService.BindSummarizer(&llm.RegistrySummarizer{
			Scope:      llm.SettingsScopeGlobal,
			ProjectDir: dir,
			Cost:       s.Sessions.CostTracker(),
			Fallback:   compaction.UnavailableSummarizer{},
			Purpose:    "commit_draft",
			Class:      llm.UtilityClassRequested,
		})
	}
	system, err := guidance.RenderCatalog(ctx, guidance.UtilityCommitMessageSystemRef, nil)
	if err != nil {
		return "", err
	}
	msg, err := summarizer.Summarize(ctx, system, diff, 120)
	if err != nil {
		return "", err
	}
	if msg = firstLine(msg); msg == "" {
		return "", errEmptyDraft
	}
	return msg, nil
}

// firstLine trims a model reply to a single subject line.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(strings.Trim(s, "`\"'"))
}
