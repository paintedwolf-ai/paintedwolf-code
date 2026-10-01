package events_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPublishKeyForWithoutLookup(t *testing.T) {
	key := events.PublishKeyFor(context.Background(), nil, "/tmp/proj", "sess-1")
	if err := events.ValidatePublishScope(api.EventTopicSession, key); err == nil {
		t.Fatalf("key = %+v", key)
	}
}

func TestScopeLookupProjectID(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "reg.Open failed", err)
	lookup := project.ScopeLookup{Registry: reg}

	id, _, claimed, err := lookup.ResolveProject(ctx, p.ID)
	if err != nil || id != p.ID || !claimed {
		t.Fatalf("by id = %q err=%v", id, err)
	}
	id, _, claimed, err = lookup.ResolveProject(ctx, "unknown")
	if err != nil || id != "unknown" || claimed {
		t.Fatalf("unknown = %q err=%v", id, err)
	}
}

func TestResolveProjectDir(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "reg.Open failed", err)
	lookup := project.ScopeLookup{Registry: reg}
	rootPath := project.PrimaryRootPath(p)

	_, got, _, err := lookup.ResolveProject(ctx, p.ID)
	testutil.FailErr(t, "resolve project", err)
	if got != rootPath {
		t.Fatalf("by id = %q want %q", got, rootPath)
	}
	_, got, _, err = lookup.ResolveProject(ctx, "/other")
	testutil.FailErr(t, "resolve unknown project", err)
	if got != "" {
		t.Fatalf("unknown project = %q want empty", got)
	}
}

type stubBoardSource struct {
	called    bool
	dir       string
	sessionID string
}

func (s *stubBoardSource) BuildView(_ context.Context, projectID, workspacePath, sessionID string, level api.BoardDetailLevel) (api.BoardView, error) {
	s.called = true
	s.dir = workspacePath
	s.sessionID = sessionID
	return api.BoardView{DetailLevel: level, SessionID: sessionID}, nil
}

func TestPublishBoardUsesResolvedProjectDir(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "reg.Open failed", err)
	hub := events.NewMemoryHub()
	board := &stubBoardSource{}
	pub := &events.Publisher{
		Hub:    hub,
		Lookup: project.ScopeLookup{Registry: reg},
		Board:  board,
	}
	rootPath := project.PrimaryRootPath(p)
	pub.PublishBoard(ctx, p.ID, "sess-1")
	if !board.called || board.dir != rootPath || board.sessionID != "sess-1" {
		t.Fatalf("board dir = %q session = %q called=%v want dir=%q session=sess-1", board.dir, board.sessionID, board.called, rootPath)
	}
}

func TestPublishBoardFollowsSessionActiveRoot(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	primary := t.TempDir()
	secondary := t.TempDir()
	p, err := project.CreateWithRoot(ctx, reg, primary)
	testutil.FailErr(t, "create", err)
	_, err = reg.AttachRoot(ctx, p.ID, project.AttachRootParams{Path: secondary, Label: "secondary"})
	testutil.FailErr(t, "attach", err)

	hub := events.NewMemoryHub()
	boardSrc := &stubBoardSource{}
	pub := &events.Publisher{
		Hub:    hub,
		Lookup: project.ScopeLookup{Registry: reg},
		Board:  boardSrc,
		SessionRoots: events.FuncSessionRoots(func(_ context.Context, sessionID string) (string, bool) {
			if sessionID == "sess-secondary" {
				return secondary, true
			}
			return "", false
		}),
	}
	pub.PublishBoard(ctx, p.ID, "sess-secondary")
	if !boardSrc.called || boardSrc.dir != secondary {
		t.Fatalf("dir=%q called=%v want %q", boardSrc.dir, boardSrc.called, secondary)
	}
}

func TestPublishBoardFallsBackWhenSessionRootsUnresolved(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "create", err)
	hub := events.NewMemoryHub()
	boardSrc := &stubBoardSource{}
	rootPath := project.PrimaryRootPath(p)
	pub := &events.Publisher{
		Hub:    hub,
		Lookup: project.ScopeLookup{Registry: reg},
		Board:  boardSrc,
		SessionRoots: events.FuncSessionRoots(func(context.Context, string) (string, bool) {
			return "", false
		}),
	}
	pub.PublishBoard(ctx, p.ID, "sess-unknown")
	if !boardSrc.called || boardSrc.dir != rootPath {
		t.Fatalf("dir=%q called=%v want primary %q", boardSrc.dir, boardSrc.called, rootPath)
	}
}
