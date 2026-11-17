package sourceapi

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOutsideReadsNeedAChatThatHoldsTheFile(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, database, "p1")
	ledger := sourceledger.New(database, filepath.Join(t.TempDir(), "content"))
	var outside string
	testutil.FailErr(t, "read the outside namespace", database.QueryRowContext(ctx, `SELECT id FROM project_roots WHERE project_id='p1' AND kind='outside'`).Scan(&outside))
	_, err := database.ExecContext(ctx, `INSERT INTO sessions (id,project_id,owner_person_id,posture,created_at,activity_at,updated_at)
		VALUES ('chat','p1',(SELECT id FROM people WHERE role = 'owner'),'build','2026-09-13T00:00:00Z','2026-09-13T00:00:00Z','2026-09-13T00:00:00Z')`)
	testutil.FailErr(t, "create chat", err)
	testutil.FailErr(t, "record outside write", ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: outside, Path: "etc/app.conf"},
		ProjectID:      "p1", BranchID: sourcebranch.Trunk, SessionID: "chat",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, After: []byte("a=1\n")}))
	p := &project.Project{ID: "p1", Outside: project.Root{ID: outside, ProjectID: "p1", Path: "/", Kind: project.RootKindOutside}}

	admitted, err := admitOutside(ctx, ledger, p, outside, "etc/app.conf")
	testutil.FailErr(t, "admit the held file", err)
	if roots := admitted.SourceRoots(); len(roots) != 1 || roots[0].ID != outside {
		t.Fatalf("admitted view roots = %+v", roots)
	}
	if len(p.Roots) != 0 {
		t.Fatal("admission changed the project")
	}
	for _, rel := range []string{"etc/other.conf", "etc/../etc/app.conf", "/etc/app.conf", ""} {
		if _, err := admitOutside(ctx, ledger, p, outside, rel); !errors.Is(err, projectsource.ErrSourcePathDenied) {
			t.Fatalf("admitOutside(%q) = %v, want denial", rel, err)
		}
	}
	unchanged, err := admitOutside(ctx, ledger, p, "folder-root", "etc/other.conf")
	testutil.FailErr(t, "pass a folder root through", err)
	if unchanged != p {
		t.Fatal("a folder root request was rewritten")
	}
}
