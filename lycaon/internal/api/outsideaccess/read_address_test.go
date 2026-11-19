//go:build darwin || linux

package outsideaccess

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveOutsideFileNeedsAHeldIdentity(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	const projectID, chatID = "p1", "chat"
	testdbseed.InsertProject(t, database, projectID)
	testdbseed.InsertSession(t, database, chatID, projectID)
	outside, err := db.New(database).GetProjectOutsideRootID(ctx, projectID)
	testutil.FailErr(t, "read outside namespace", err)
	ledger := sourceledger.New(database, t.TempDir())

	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve fixture path", err)
	name := filepath.Join(dir, "notes.txt")
	testutil.FailErr(t, "write file", os.WriteFile(name, []byte("notes"), 0o600))
	rel, ok := projectroot.OutsideRel(name)
	if !ok {
		t.Fatal("fixture has no outside address")
	}
	input := sourceledger.TrackInput{ProjectID: projectID, RootID: outside, Path: rel, Content: []byte("notes")}
	file, err := ledger.TrackChatFile(ctx, input, chatID, sourceledger.ChatFileRead)
	testutil.FailErr(t, "track outside file", err)
	p := &project.Project{ID: projectID, Outside: project.Root{ID: outside, ProjectID: projectID, Path: "/", Kind: project.RootKindOutside}}

	scoped, target, err := Resolve(ctx, ledger, p, Address{FileID: file.FileID})
	testutil.FailErr(t, "resolve held identity", err)
	if target.Path != rel || target.RootID != outside || scoped.OutsideReadBinding().Directory != dir || scoped.OutsideReadBinding().Identity == "" {
		t.Fatalf("resolved address = %+v with binding %+v", target, scoped.OutsideReadBinding())
	}
	if len(p.Roots) != 0 {
		t.Fatal("admission mutated the project")
	}
	for _, tc := range []struct {
		target Address
		err    error
	}{
		{Address{RootID: outside, Path: rel}, projectsource.ErrSourcePathDenied},
		{Address{FileID: file.FileID, Path: name}, projectsource.ErrSourcePathInvalid},
		{Address{FileID: "bad-id"}, projectsource.ErrSourcePathInvalid},
		{Address{FileID: uuid.NewString()}, projectsource.ErrSourceNotFound},
	} {
		if _, _, err := Resolve(ctx, ledger, p, tc.target); !errors.Is(err, tc.err) {
			t.Fatalf("Resolve(%+v) = %v, want %v", tc.target, err, tc.err)
		}
	}
	attached := Address{RootID: "folder-root", Path: "notes.txt"}
	unchanged, target, err := Resolve(ctx, ledger, p, attached)
	testutil.FailErr(t, "pass a folder root through", err)
	if unchanged != p || target != attached {
		t.Fatalf("folder request was rewritten to %+v", target)
	}
}
