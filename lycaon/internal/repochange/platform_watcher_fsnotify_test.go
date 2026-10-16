//go:build !darwin || !cgo

package repochange

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/testutil"
)

func subscribeForTest(t *testing.T, m *fsnotifyMux) *fsnotifyPlatformWatcher {
	t.Helper()
	w, err := m.subscribe()
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// awaitEventNamed drains w until an event names want.
func awaitEventNamed(t *testing.T, w *fsnotifyPlatformWatcher, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-w.events:
			if ev.Name == want {
				return
			}
		case err := <-w.errors:
			t.Fatalf("stream error while waiting for %s: %v", want, err)
		case <-deadline:
			t.Fatalf("no event named %s", want)
		}
	}
}

// awaitNoEventNamed reports a delivery of unwanted within a short window.
func awaitNoEventNamed(t *testing.T, w *fsnotifyPlatformWatcher, unwanted string) {
	t.Helper()
	window := time.After(200 * time.Millisecond)
	for {
		select {
		case ev := <-w.events:
			if ev.Name == unwanted {
				t.Fatalf("unexpected event %s", unwanted)
			}
		case <-window:
			return
		}
	}
}

func TestRootsShareOneStreamAndKeepTheirOwnRegistrations(t *testing.T) {
	m := &fsnotifyMux{}
	dir := t.TempDir()
	first := subscribeForTest(t, m)
	second := subscribeForTest(t, m)
	stream := m.fw
	testutil.FailErr(t, "first add", first.Add(dir))
	testutil.FailErr(t, "second add", second.Add(dir))

	testutil.FailErr(t, "write one", os.WriteFile(filepath.Join(dir, "one"), nil, 0o644))
	awaitEventNamed(t, first, filepath.Join(dir, "one"))
	awaitEventNamed(t, second, filepath.Join(dir, "one"))

	// Releasing one root's registration keeps the directory watched for the other.
	testutil.FailErr(t, "first remove", first.Remove(dir))
	if !errors.Is(first.Remove(dir), fsnotify.ErrNonExistentWatch) {
		t.Fatal("removing an unregistered path succeeded")
	}
	if len(first.WatchList()) != 0 || !slices.Equal(second.WatchList(), []string{dir}) {
		t.Fatalf("watch lists = %v and %v", first.WatchList(), second.WatchList())
	}
	testutil.FailErr(t, "write two", os.WriteFile(filepath.Join(dir, "two"), nil, 0o644))
	awaitEventNamed(t, second, filepath.Join(dir, "two"))
	awaitNoEventNamed(t, first, filepath.Join(dir, "two"))

	testutil.FailErr(t, "first close", first.Close())
	testutil.FailErr(t, "first close again", first.Close())
	if !errors.Is(first.Add(dir), fsnotify.ErrClosed) {
		t.Fatal("closed root registered a directory")
	}
	if m.fw != stream {
		t.Fatal("closing one root replaced the shared stream")
	}
	testutil.FailErr(t, "write three", os.WriteFile(filepath.Join(dir, "three"), nil, 0o644))
	awaitEventNamed(t, second, filepath.Join(dir, "three"))

	testutil.FailErr(t, "second close", second.Close())
	if m.fw != nil {
		t.Fatal("the last root left the shared stream open")
	}
	again := subscribeForTest(t, m)
	if m.fw == nil || m.fw == stream {
		t.Fatal("a root after the last close did not open a fresh stream")
	}
	testutil.FailErr(t, "close fresh root", again.Close())
}

// Closing a root returns its registrations to the stream, so a directory only
// it watched stops producing events for anyone.
func TestClosingARootReleasesOnlyItsDirectories(t *testing.T) {
	m := &fsnotifyMux{}
	shared, private := t.TempDir(), t.TempDir()
	keeper := subscribeForTest(t, m)
	leaver := subscribeForTest(t, m)
	testutil.FailErr(t, "keeper add", keeper.Add(shared))
	testutil.FailErr(t, "leaver add shared", leaver.Add(shared))
	testutil.FailErr(t, "leaver add private", leaver.Add(private))
	testutil.FailErr(t, "leaver close", leaver.Close())
	if got := m.fw.WatchList(); !slices.Equal(got, []string{streamPath(shared)}) {
		t.Fatalf("stream watches %v after close, want only the shared directory", got)
	}
	testutil.FailErr(t, "write shared", os.WriteFile(filepath.Join(shared, "kept"), nil, 0o644))
	awaitEventNamed(t, keeper, filepath.Join(shared, "kept"))
}

// inotify names events after the first path registered for an inode; each root
// still sees its own spelling.
func TestAliasedDirectoriesReportEachRootsSpelling(t *testing.T) {
	m := &fsnotifyMux{}
	physical := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	testutil.FailErr(t, "symlink", os.Symlink(physical, alias))
	direct := subscribeForTest(t, m)
	linked := subscribeForTest(t, m)
	testutil.FailErr(t, "direct add", direct.Add(physical))
	testutil.FailErr(t, "linked add", linked.Add(alias))

	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(physical, "f"), nil, 0o644))
	awaitEventNamed(t, direct, filepath.Join(physical, "f"))
	awaitEventNamed(t, linked, filepath.Join(alias, "f"))

	// Re-registering after the alias moves follows the new target only, and
	// releases the old one once no root listens there.
	testutil.FailErr(t, "direct close", direct.Close())
	moved := t.TempDir()
	testutil.FailErr(t, "unlink alias", os.Remove(alias))
	testutil.FailErr(t, "retarget alias", os.Symlink(moved, alias))
	testutil.FailErr(t, "linked re-add", linked.Add(alias))
	if slices.Contains(m.fw.WatchList(), streamPath(physical)) {
		t.Fatal("the stream still watches the alias's former target")
	}
	testutil.FailErr(t, "write old target", os.WriteFile(filepath.Join(physical, "g"), nil, 0o644))
	testutil.FailErr(t, "write new target", os.WriteFile(filepath.Join(moved, "h"), nil, 0o644))
	awaitEventNamed(t, linked, filepath.Join(alias, "h"))
	awaitNoEventNamed(t, linked, filepath.Join(alias, "g"))
}

// A root that stops draining loses events as an overflow; dispatch never waits
// on it, so other roots keep receiving.
func TestLaggingRootOverflowsWithoutBlockingOthers(t *testing.T) {
	m := &fsnotifyMux{}
	lagging := subscribeForTest(t, m)
	draining := subscribeForTest(t, m)
	m.mu.Lock()
	m.routes["/lag"] = map[*fsnotifyPlatformWatcher]string{lagging: "/lag"}
	m.routes["/drain"] = map[*fsnotifyPlatformWatcher]string{draining: "/drain"}
	m.mu.Unlock()
	for range rootEventBuffer + 1 {
		m.route(fsnotify.Event{Name: "/lag/f", Op: fsnotify.Write})
	}
	m.route(fsnotify.Event{Name: "/drain/f", Op: fsnotify.Write})
	if ev := <-draining.events; ev.Name != "/drain/f" {
		t.Fatalf("draining root received %+v", ev)
	}
	if err := <-lagging.errors; !errors.Is(err, fsnotify.ErrEventOverflow) {
		t.Fatalf("lagging root error = %v, want an overflow", err)
	}
	if len(draining.errors) != 0 {
		t.Fatal("the draining root was told about another root's overflow")
	}
}

func TestStreamErrorsReachEveryRoot(t *testing.T) {
	m := &fsnotifyMux{}
	roots := []*fsnotifyPlatformWatcher{subscribeForTest(t, m), subscribeForTest(t, m)}
	failure := errors.New("stream failed")
	for range rootErrorBuffer + 1 {
		m.broadcast(failure)
	}
	for _, w := range roots {
		if len(w.errors) != rootErrorBuffer || !errors.Is(<-w.errors, failure) {
			t.Fatal("a root missed the stream error")
		}
	}
}

func TestStreamPathResolvesLinksAndKeepsMissingPaths(t *testing.T) {
	physical := t.TempDir()
	resolved, err := filepath.EvalSymlinks(physical)
	testutil.FailErr(t, "resolve", err)
	alias := filepath.Join(t.TempDir(), "alias")
	testutil.FailErr(t, "symlink", os.Symlink(physical, alias))
	if got := streamPath(alias); got != resolved {
		t.Fatalf("streamPath(alias) = %s, want %s", got, resolved)
	}
	missing := filepath.Join(physical, "gone", "..", "missing")
	if got := streamPath(missing); got != filepath.Join(physical, "missing") {
		t.Fatalf("streamPath(missing) = %s", got)
	}
}
