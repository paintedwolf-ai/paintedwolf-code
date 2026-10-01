package pagesession

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fixtureDir(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "test", "fixtures", "capture-page", name))
	testutil.FailErr(t, "fixture abs", err)
	return root
}

func TestRegistryOpenCapCloseAndSessionEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("browser page session")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := browser.NewPool("")
	defer pool.Close()

	reg := NewRegistry(Config{MaxPages: 2, IdleTimeout: time.Hour})
	defer reg.Close(t.Context())

	ctx := context.Background()
	const sid = "sess-pages"
	openOne := func() *Entry {
		t.Helper()
		held, err := browser.OpenHeld(ctx, pool, browser.OpenOpts{
			ProjectDir: fixtureDir(t, "clean"),
			Width:      320, Height: 240,
		})
		testutil.FailErr(t, "OpenHeld", err)
		e, err := reg.Open(ctx, sid, held)
		testutil.FailErr(t, "Open", err)
		return e
	}

	e1 := openOne()
	e2 := openOne()
	if reg.CountLive(sid) != 2 {
		t.Fatalf("live=%d want 2", reg.CountLive(sid))
	}
	held, err := browser.OpenHeld(ctx, pool, browser.OpenOpts{
		ProjectDir: fixtureDir(t, "clean"),
		Width:      320, Height: 240,
	})
	testutil.FailErr(t, "OpenHeld overflow", err)
	_, err = reg.Open(ctx, sid, held)
	if !errors.Is(err, ErrPageCapReached) {
		t.Fatalf("cap err=%v want ErrPageCapReached", err)
	}
	if _, err := reg.RequireRunning(sid, e1.ID); err != nil {
		t.Fatalf("RequireRunning: %v", err)
	}
	testutil.FailErr(t, "ClosePage", reg.ClosePage(ctx, sid, e2.ID))
	if reg.CountLive(sid) != 1 {
		t.Fatalf("after close live=%d", reg.CountLive(sid))
	}
	testutil.FailErr(t, "dispose session pages", reg.DisposeSession(ctx, sid))
	if reg.CountLive(sid) != 0 {
		t.Fatalf("after session end live=%d", reg.CountLive(sid))
	}
	if _, err := reg.RequireRunning(sid, e1.ID); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("after session end require=%v", err)
	}
}

func TestRegistryIdleReap(t *testing.T) {
	if testing.Short() {
		t.Skip("browser page session")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := browser.NewPool("")
	defer pool.Close()

	reg := NewRegistry(Config{MaxPages: 2, IdleTimeout: 50 * time.Millisecond})
	defer reg.Close(t.Context())

	held, err := browser.OpenHeld(context.Background(), pool, browser.OpenOpts{
		ProjectDir: fixtureDir(t, "clean"),
		Width:      320, Height: 240,
	})
	testutil.FailErr(t, "OpenHeld", err)
	e, err := reg.Open(context.Background(), "sess-idle", held)
	testutil.FailErr(t, "Open", err)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		reg.reapIdle()
		if reg.CountLive("sess-idle") == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reg.CountLive("sess-idle") != 0 {
		t.Fatal("expected idle reap")
	}
	if _, err := reg.RequireRunning("sess-idle", e.ID); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("require after reap: %v", err)
	}
}

func TestRegistryFindByTargetAndTargetURL(t *testing.T) {
	if testing.Short() {
		t.Skip("browser page session")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := browser.NewPool("")
	defer pool.Close()

	reg := NewRegistry(Config{MaxPages: 2, IdleTimeout: time.Hour})
	defer reg.Close(t.Context())
	ctx := context.Background()
	dir := fixtureDir(t, "clean")

	held, err := browser.OpenHeld(ctx, pool, browser.OpenOpts{ProjectDir: dir})
	testutil.FailErr(t, "OpenHeld", err)
	entry, err := reg.Open(ctx, "sess-find", held)
	testutil.FailErr(t, "Open", err)

	if got := reg.TargetURL("sess-find", entry.ID); got != entry.TargetURL {
		t.Fatalf("TargetURL = %q, want %q", got, entry.TargetURL)
	}
	if got := reg.TargetURL("sess-find", "no-such-page"); got != "" {
		t.Fatalf("unknown page id returned %q", got)
	}
	// Reading a target does not extend its lifetime.
	if entry.LastUsed != entry.OpenedAt {
		t.Fatal("TargetURL bumped LastUsed")
	}

	target := browser.PageTarget{URL: entry.TargetURL, RootDir: held.RootDir}
	id, ok := reg.FindByTarget("sess-find", target)
	if !ok || id != entry.ID {
		t.Fatalf("FindByTarget = (%q, %v), want (%q, true)", id, ok, entry.ID)
	}
	if _, ok := reg.FindByTarget("sess-find", browser.PageTarget{URL: "http://127.0.0.1:1/"}); ok {
		t.Fatal("FindByTarget matched an unheld target")
	}
	// Every project_dir page shares one synthetic origin; the served tree tells them apart.
	if _, ok := reg.FindByTarget("sess-find", browser.PageTarget{URL: entry.TargetURL, RootDir: t.TempDir()}); ok {
		t.Fatal("FindByTarget matched a different project tree at the same URL")
	}
	if _, ok := reg.FindByTarget("other-session", target); ok {
		t.Fatal("FindByTarget crossed a session boundary")
	}
}

func TestCapacitySnapshotIsSessionScopedAndStable(t *testing.T) {
	r := &Registry{cfg: Config{MaxPages: 3}, sessions: map[string]map[string]*Entry{
		"own": {"c": {}, "a": {}, "b": {}}, "other": {"private": {}},
	}}
	r.mu.Lock()
	refusal := r.capacityErrorLocked("own")
	delete(r.sessions["own"], "a")
	r.mu.Unlock()
	if !errors.Is(refusal, ErrPageCapReached) || refusal.Limit != 3 || len(refusal.IDs) != 3 || refusal.IDs[0] != "a" || refusal.IDs[1] != "b" || refusal.IDs[2] != "c" {
		t.Fatalf("capacity refusal changed or leaked sessions: %+v", refusal)
	}
}
