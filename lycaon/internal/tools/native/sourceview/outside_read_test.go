//go:build darwin || linux

package sourceview

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestOutsideReadRecordsAnOrdinaryPathButNotScratch(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	const projectID, chatID = "p1", "chat"
	projectFolder := t.TempDir()
	rootID := testdbseed.InsertProjectRoot(t, database, projectID, projectFolder)
	testdbseed.InsertSession(t, database, chatID, projectID)
	outsideID, err := db.New(database).GetProjectOutsideRootID(ctx, projectID)
	testutil.FailErr(t, "read outside namespace", err)
	ledger := sourceledger.New(database, t.TempDir())
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: projectID, SessionID: chatID},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: rootID, Path: projectFolder}},
			OutsideRoot:  projectroot.OutsideRef(outsideID),
			SourceLedger: ledger,
		},
	}

	home := os.Getenv("PW_TEST_HOST_HOME")
	if home == "" {
		home, err = os.UserHomeDir()
		testutil.FailErr(t, "read home folder", err)
	}
	outside, err := os.MkdirTemp(home, ".pw-outside-read-") //nolint:usetesting // Outside history omits scratch roots.
	testutil.FailErr(t, "make outside fixture", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove outside fixture", os.RemoveAll(outside)) })
	read := func(dir string) string {
		name := filepath.Join(dir, "notes.txt")
		testutil.FailErr(t, "write notes", os.WriteFile(name, []byte("hello\n"), 0o600))
		resolved := projectpaths.Resolved{
			Root: projectroot.RootRef{Path: dir}, ScopeRel: "notes.txt",
			Abs: name, DisplayPath: name, External: true,
		}
		text, err := LoadText(ctx, "read", AccessRead, tc, resolved)
		testutil.FailErr(t, "read notes", err)
		if text.Content != "hello\n" {
			t.Fatalf("read %q", text.Content)
		}
		return name
	}
	name := read(outside)
	_ = read(t.TempDir())

	locations, err := ledger.ChatLocations(ctx, chatID)
	testutil.FailErr(t, "list chat locations", err)
	if len(locations) != 1 || locations[0].Directory() != outside {
		t.Fatalf("locations = %+v, want only %s", locations, outside)
	}
	rel, ok := projectroot.OutsideRel(name)
	if !ok {
		t.Fatal("fixture lacks an outside address")
	}
	tracked, err := ledger.LookupFile(ctx, sourceledger.TrackInput{ProjectID: projectID, RootID: outsideID, Path: rel})
	testutil.FailErr(t, "lookup file after read", err)
	root, path, err := ledger.HeldFileAddress(ctx, projectID, tracked.FileID)
	testutil.FailErr(t, "resolve the held file", err)
	if root != outsideID || path != rel {
		t.Fatalf("held file address = %q %q", root, path)
	}
}
