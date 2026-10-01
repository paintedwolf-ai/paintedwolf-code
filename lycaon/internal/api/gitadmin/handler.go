package gitadmin

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
)

// Deps are the Git routes' dependencies, fixed at construction.
type Deps struct {
	// Board carries the Git manager, status cache, and repo brief warmer.
	Board *board.SnapshotBuilder
	// CommitDrafter replaces the lite model for commit drafts; nil uses LLMService.
	CommitDrafter   compaction.Summarizer
	LLMService      *llm.Service
	ProjectRegistry project.Registry
	RepoSetCache    *git.RepoSetCache
	SessionStore    session.Store
	Sessions        *session.Manager
	DataDir         string
}

type Handler struct {
	Deps
	// git is the board's Git manager; worktree verbs need the concrete manager.
	git       *git.Manager
	responses *httpio.Responder
}

func New(responses *httpio.Responder, deps Deps) Handler {
	var manager *git.Manager
	if deps.Board != nil {
		manager, _ = deps.Board.Git.(*git.Manager)
	}
	httpio.RequireDependencies("gitadmin",
		httpio.Required{Name: "responses", Present: responses != nil},
		httpio.Required{Name: "Board", Present: deps.Board != nil},
		httpio.Required{Name: "Board.Git", Present: manager != nil},
		httpio.Required{Name: "Board.Repo", Present: deps.Board != nil && deps.Board.Repo != nil},
		httpio.Required{Name: "Board.StatusCache", Present: deps.Board != nil && deps.Board.StatusCache != nil},
		httpio.Required{Name: "LLMService", Present: deps.LLMService != nil},
		httpio.Required{Name: "ProjectRegistry", Present: deps.ProjectRegistry != nil},
		httpio.Required{Name: "SessionStore", Present: deps.SessionStore != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
	)
	return Handler{Deps: deps, git: manager, responses: responses}
}

// Manager returns the board's Git manager, the one every read goes through.
func (s *Handler) Manager() git.GitManager {
	return s.Board.Git
}

func (s *Handler) WarmRepoBrief(projectDir string) {
	s.Board.Repo.Warm(strings.TrimSpace(projectDir))
}

// WarmStatus refreshes shared status asynchronously.
func (s *Handler) WarmStatus(background *taskgroup.Group, ctx context.Context, dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	background.Go(ctx, func(ctx context.Context) {
		if _, err := s.Board.StatusCache.GetOrLoad(ctx, dir, false); err != nil {
			slog.DebugContext(ctx, "warm git status", "path", dir, "err", err)
		}
	})
}
