package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Command custody written into a folder other than the session folder keeps
// that folder's identity, so every spelling of the path finds the custody.
func TestProgramCustodyKeepsRootIdentityOutsideSessionFolder(t *testing.T) {
	base := t.TempDir()
	session := filepath.Join(base, "app")
	other := filepath.Join(base, "vendor")
	for _, dir := range []string{session, other} {
		testutil.FailErr(t, "mkdir root", os.MkdirAll(dir, 0o755))
	}
	database := testdbfixture.OpenPath(t, filepath.Join(base, "custody-roots.db"))
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, session)
	otherID := testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "vendor", other)
	owner := NewSQL(database)
	owner.SetDataDir(base)
	sess, err := owner.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	const url = "https://downloads.example/payload.tar"
	handle, err := owner.CommitProgramHTTPProvenance(t.Context(), sess.ID, session, evidence.ProgramHTTPProvenance{
		ProgramID:    "program:custody",
		ObservedURLs: []string{url},
		CustodyPaths: []string{"@vendor/payload.tar"},
	})
	testutil.FailErr(t, "commit program custody", err)

	ledger, err := owner.LoadLedger(t.Context(), sess.ID)
	testutil.FailErr(t, "load ledger", err)
	rec, ok := evidence.ResolveHandle(ledger, handle)
	if !ok {
		t.Fatal("program custody record missing")
	}
	if rec.RootID != otherID || rec.Path != "payload.tar" {
		t.Fatalf("custody identity = root %q path %q, want root %q path payload.tar", rec.RootID, rec.Path, otherID)
	}

	for _, spelling := range []string{"@vendor/payload.tar", filepath.Join(other, "payload.tar")} {
		urls, err := owner.ExternalCustodyURLsForPaths(t.Context(), sess.ID, session, []string{spelling})
		testutil.FailErr(t, "custody lookup", err)
		if len(urls) != 1 || urls[0] != url {
			t.Fatalf("custody for %q = %v, want [%s]", spelling, urls, url)
		}
	}
	// The same relative path in the session folder is a different file.
	for _, spelling := range []string{"payload.tar", filepath.Join(session, "payload.tar")} {
		urls, err := owner.ExternalCustodyURLsForPaths(t.Context(), sess.ID, session, []string{spelling})
		testutil.FailErr(t, "session-folder custody lookup", err)
		if len(urls) != 0 {
			t.Fatalf("custody for %q = %v, want none", spelling, urls)
		}
	}

	// Reading each file into model context carries custody only from its own folder.
	read := func(token string) evidence.Record {
		t.Helper()
		body := `{"path":"` + token + `","content":"1|bytes\n","offset":1,"end_line":1}`
		h, _, err := owner.CommitEvidenceToolResult(t.Context(), sess.ID, session, "read", map[string]any{"path": token}, body)
		testutil.FailErr(t, "read "+token, err)
		ledger, err := owner.LoadLedger(t.Context(), sess.ID)
		testutil.FailErr(t, "load ledger after read", err)
		rec, ok := evidence.ResolveHandle(ledger, h)
		if !ok {
			t.Fatalf("read record for %q missing", token)
		}
		return rec
	}
	if rec := read("payload.tar"); rec.UntrustedContent {
		t.Fatal("reading the session folder's payload.tar must not inherit another folder's custody")
	}
	if rec := read("@vendor/payload.tar"); !rec.UntrustedContent {
		t.Fatal("reading @vendor/payload.tar must carry its download custody")
	}
}
