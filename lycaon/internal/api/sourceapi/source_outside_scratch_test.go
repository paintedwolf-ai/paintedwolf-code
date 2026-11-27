//go:build darwin || linux

package sourceapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A person may open files in the chat's own scratch, never other engine state
// or another chat's scratch, and program temp has no history to open.
func TestOpenedOutsideFileStaysOutOfEngineState(t *testing.T) {
	state := fspath.CanonicalPath(t.TempDir())
	t.Setenv(configdir.EnvConfigDir, state)
	db := testdbfixture.Open(t, "store.db")
	sessions := store.NewSQL(db)
	server := newSourceHandlerFixture(t, func(deps *Deps) {
		deps.SessionStore, deps.ProjectRegistry = sessions, project.NewSQLRegistry(db)
		deps.SourceLedger = sourceledger.New(db, t.TempDir(), sourceledger.Timing{})
	})
	p := sourceViewFixtureProject(t, server)
	ctx := testdbseed.OwnerCaller(t, t.Context(), db)
	openChat := func() string {
		chat, err := sessions.Sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, p.ID)
		testutil.FailErr(t, "create chat", err)
		return chat.ID
	}
	chat, other := openChat(), openChat()
	write := func(path string) string {
		testutil.FailErr(t, "make folder", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write file", os.WriteFile(path, []byte("bytes\n"), 0o600))
		return path
	}
	scratch := enginepaths.SessionScratchUnder(state, chat)
	open := func(path string) *httptest.ResponseRecorder {
		return callSourceViewHandlerAs(t, ctx, server.Workspace.HandleOpenOutsideFile, p.ID, "", wire.OpenOutsideFileRequest{SessionID: chat, Path: path})
	}

	opened := readOpenedFile(t, open(write(filepath.Join(scratch, "notes.md"))))
	location := opened.Location
	if location.Label != "@scratch" || location.Path != scratch || location.Access == nil || location.Access.Mode != "write" {
		t.Fatalf("scratch location = %+v", location)
	}
	for name, path := range map[string]string{
		"engine state":       write(filepath.Join(state, "store.db")),
		"another scratch":    write(filepath.Join(enginepaths.SessionScratchUnder(state, other), "secret.txt")),
		"program temp":       write(filepath.Join(enginepaths.ProgramTempUnder(scratch), "a.o")),
		"another session id": write(filepath.Join(enginepaths.ScratchRootUnder(state), "not-a-session", "x.txt")),
	} {
		if got := open(path); got.Code != http.StatusBadRequest {
			t.Errorf("%s opened with %d: %s", name, got.Code, got.Body.String())
		}
	}
}
