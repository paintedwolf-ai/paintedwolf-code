package clisocket

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakeProjects struct {
	projects []project.Project
	err      error
}

func (f fakeProjects) List(context.Context) ([]project.Project, error) {
	return f.projects, f.err
}

type fakePublisher struct {
	mu     sync.Mutex
	events []api.CLIOpenEvent
}

func (f *fakePublisher) PublishCLIOpen(_ context.Context, ev api.CLIOpenEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
}

func (f *fakePublisher) all() []api.CLIOpenEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.CLIOpenEvent(nil), f.events...)
}

func newServer(projects []project.Project) (*Server, *fakePublisher) {
	pub := &fakePublisher{}
	return &Server{projects: fakeProjects{projects: projects}, publish: pub}, pub
}

// shortConfigDir stays within the socket path limit.
func shortConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cs") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "temp config dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(configdir.EnvConfigDir, dir)
	return dir
}

func TestHandleListSortsMostRecentFirst(t *testing.T) {
	now := time.Now()
	s, _ := newServer([]project.Project{
		proj("old", "old", now.Add(-time.Hour), root(t.TempDir())),
		proj("new", "new", now, root(t.TempDir())),
	})

	resp := s.Handle(context.Background(), Request{Op: OpList})

	if resp.Status != StatusOK {
		t.Fatalf("status = %q: %s", resp.Status, resp.Message)
	}
	if len(resp.Projects) != 2 || resp.Projects[0].ID != "new" {
		t.Fatalf("rows = %+v, want new first", resp.Projects)
	}
}

func TestHandleOpenPublishesResolvedProject(t *testing.T) {
	dir := t.TempDir()
	s, pub := newServer([]project.Project{proj("p1", "app", time.Now(), root(dir))})

	resp := s.Handle(context.Background(), Request{Op: OpOpen, Target: dir})

	if resp.Status != StatusOK || resp.Action != api.CLIOpenActionOpen {
		t.Fatalf("resp = %+v, want ok/open", resp)
	}
	events := pub.all()
	if len(events) != 1 || events[0].ProjectID == nil || *events[0].ProjectID != "p1" {
		t.Fatalf("published = %+v, want one open for p1", events)
	}
	if len(resp.Projects) != 1 || resp.Projects[0].Name != "app" {
		t.Fatalf("resp.Projects = %+v, want the resolved project named app", resp.Projects)
	}
}

func TestHandleOpenIsIdempotentWithoutNewFlag(t *testing.T) {
	dir := t.TempDir()
	s, pub := newServer([]project.Project{proj("p1", "app", time.Now(), root(dir))})

	for range 2 {
		s.Handle(context.Background(), Request{Op: OpOpen, Target: dir})
	}

	for _, ev := range pub.all() {
		if ev.Action != api.CLIOpenActionOpen {
			t.Fatalf("action = %q, want every repeat to open the same project", ev.Action)
		}
	}
}

func TestHandleOpenNewForcesCreateOnAKnownFolder(t *testing.T) {
	dir := t.TempDir()
	s, pub := newServer([]project.Project{proj("p1", "app", time.Now(), root(dir))})

	resp := s.Handle(context.Background(), Request{Op: OpOpen, Target: dir, New: true})

	if resp.Action != api.CLIOpenActionCreate {
		t.Fatalf("action = %q, want create under --new", resp.Action)
	}
	events := pub.all()
	if len(events) != 1 || events[0].ProjectID != nil {
		t.Fatalf("published = %+v, want one create with no project", events)
	}
}

func TestHandleOpenAmbiguousNameRefusesToGuess(t *testing.T) {
	now := time.Now()
	s, pub := newServer([]project.Project{
		proj("a", "app", now, root(t.TempDir())),
		proj("b", "app", now.Add(-time.Hour), root(t.TempDir())),
	})

	resp := s.Handle(context.Background(), Request{Op: OpOpen, Target: "app"})

	if resp.Status != StatusAmbiguous {
		t.Fatalf("status = %q, want ambiguous", resp.Status)
	}
	if len(resp.Projects) != 2 {
		t.Fatalf("rows = %d, want both candidates listed", len(resp.Projects))
	}
	if events := pub.all(); len(events) != 0 {
		t.Fatalf("published %d events, want none on an ambiguous name", len(events))
	}
}

func TestHandleOpenUnknownNameErrors(t *testing.T) {
	s, pub := newServer(nil)

	resp := s.Handle(context.Background(), Request{Op: OpOpen, Target: "nope"})

	if resp.Status != StatusError {
		t.Fatalf("status = %q, want error", resp.Status)
	}
	if !strings.Contains(resp.Message, "`pw ls`") {
		t.Fatalf("recovery message = %q, want the shipped project-list command", resp.Message)
	}
	if events := pub.all(); len(events) != 0 {
		t.Fatalf("published %d events for an unknown project", len(events))
	}
}

func TestHandleOpenMissingFolderErrors(t *testing.T) {
	s, pub := newServer(nil)
	missing := filepath.Join(t.TempDir(), "gone")

	resp := s.Handle(context.Background(), Request{Op: OpOpen, Target: missing})

	if resp.Status != StatusError {
		t.Fatalf("status = %q, want error for a folder that is not there", resp.Status)
	}
	if events := pub.all(); len(events) != 0 {
		t.Fatalf("published %d events, want none", len(events))
	}
}

func TestHandleOpenFileTellsYouToUseItsFolder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "main.go")
	testutil.FailErr(t, "write file", os.WriteFile(file, []byte("package main"), 0o600))
	s, _ := newServer(nil)

	resp := s.Handle(context.Background(), Request{Op: OpOpen, Target: file})

	if resp.Status != StatusError {
		t.Fatalf("status = %q, want error", resp.Status)
	}
}

func TestHandleUnknownOpErrors(t *testing.T) {
	s, _ := newServer(nil)

	if resp := s.Handle(context.Background(), Request{Op: "nope"}); resp.Status != StatusError {
		t.Fatalf("status = %q, want error", resp.Status)
	}
}

func TestListenRoundTripsOverTheSocket(t *testing.T) {
	shortConfigDir(t)
	dir := t.TempDir()
	pub := &fakePublisher{}
	projects := fakeProjects{projects: []project.Project{
		proj("p1", "app", time.Now(), root(dir)),
	}}

	srv, err := Listen(context.Background(), projects, pub)
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = srv.Close() })

	resp := roundTrip(t, srv.Path(), Request{Op: OpOpen, Target: dir})

	if resp.Status != StatusOK || resp.Action != api.CLIOpenActionOpen {
		t.Fatalf("resp = %+v, want ok/open", resp)
	}
	if events := pub.all(); len(events) != 1 {
		t.Fatalf("published %d events, want 1", len(events))
	}
}

// Owner-only mode is the socket's authorization boundary.
func TestListenSocketIsOwnerOnly(t *testing.T) {
	shortConfigDir(t)
	srv, err := Listen(context.Background(), fakeProjects{}, &fakePublisher{})
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = srv.Close() })

	info, err := os.Stat(srv.Path())
	testutil.FailErr(t, "stat socket", err)

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %o, want 600", perm)
	}
}

// One unclean exit must not break the CLI until someone deletes a file by hand.
func TestListenTakesOverASocketLeftByADeadEngine(t *testing.T) {
	dir := shortConfigDir(t)
	stale := filepath.Join(dir, SocketName)
	listener, err := net.Listen("unix", stale)
	testutil.FailErr(t, "bind stale socket", err)
	// Closing without unlinking is what a crash leaves behind.
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener type = %T, want *net.UnixListener", listener)
	}
	unixListener.SetUnlinkOnClose(false)
	testutil.FailErr(t, "close stale socket", unixListener.Close())

	srv, err := Listen(context.Background(), fakeProjects{}, &fakePublisher{})
	testutil.FailErr(t, "listen over stale socket", err)
	t.Cleanup(func() { _ = srv.Close() })

	resp := roundTrip(t, srv.Path(), Request{Op: OpList})
	if resp.Status != StatusOK {
		t.Fatalf("status = %q, want ok after taking over the stale path", resp.Status)
	}
}

func TestListenRefusesWhenAnotherEngineIsLive(t *testing.T) {
	shortConfigDir(t)
	first, err := Listen(context.Background(), fakeProjects{}, &fakePublisher{})
	testutil.FailErr(t, "first listen", err)
	t.Cleanup(func() { _ = first.Close() })

	if _, err := Listen(context.Background(), fakeProjects{}, &fakePublisher{}); err == nil {
		t.Fatal("second Listen succeeded, want refusal while the first is live")
	}
}

func TestCloseUnlinksTheSocket(t *testing.T) {
	shortConfigDir(t)
	srv, err := Listen(context.Background(), fakeProjects{}, &fakePublisher{})
	testutil.FailErr(t, "listen", err)
	path := srv.Path()

	testutil.FailErr(t, "close", srv.Close())

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket still present after Close (err = %v)", err)
	}
}

func roundTrip(t *testing.T, path string, req Request) Response {
	t.Helper()
	conn, err := net.Dial("unix", path)
	testutil.FailErr(t, "dial", err)
	defer func() { _ = conn.Close() }()
	testutil.FailErr(t, "deadline", conn.SetDeadline(time.Now().Add(5*time.Second)))
	testutil.FailErr(t, "encode", json.NewEncoder(conn).Encode(req))
	var resp Response
	testutil.FailErr(t, "decode", json.NewDecoder(conn).Decode(&resp))
	return resp
}
