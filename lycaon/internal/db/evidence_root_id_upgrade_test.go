package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// seedEvidenceRootFixture adds two workspace roots, two sessions (one pinned
// to the secondary root, one on the project default), and legacy evidence
// rows covering every stored path spelling the backfill must resolve.
func seedEvidenceRootFixture(t *testing.T, source *sql.DB) {
	t.Helper()
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	secondary := filepath.Join(base, "feature")
	for _, dir := range []string{primary, secondary, filepath.Join(secondary, "docs")} {
		testutil.FailErr(t, "create root directory", os.MkdirAll(dir, 0o755))
	}
	seed := func(query string, args ...any) {
		testutil.FailErr(t, "seed fixture", func() error {
			_, err := source.ExecContext(t.Context(), query, args...)
			return err
		}())
	}
	seed(`INSERT INTO project_roots(id,project_id,path,label,is_primary,added_at) VALUES('root-p','fixture-project',?,'main',1,'2026-01-01T00:00:00Z')`, primary)
	seed(`INSERT INTO project_roots(id,project_id,path,label,is_primary,added_at) VALUES('root-s','fixture-project',?,'feature',0,'2026-01-01T00:00:00Z')`, secondary)
	seed(`INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at,workspace_root_id) VALUES('sess-pinned','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','root-s')`)
	seed(`INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('sess-default','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)

	absSecondary := filepath.Join(secondary, "docs", "guide.md")
	seed(`INSERT INTO evidence_records(session_id,project_id,handle,ordinal,kind,path) VALUES
			('sess-pinned','fixture-project','read#1',1,'file_read','src/app.go'),
			('sess-pinned','fixture-project','read#2',2,'file_read','@feature/src/lib.go'),
			('sess-pinned','fixture-project','read#3',3,'file_read','@main/README.md'),
			('sess-pinned','fixture-project','read#4',4,'file_read',?),
			('sess-pinned','fixture-project','read#5',5,'file_read','/elsewhere/notes.txt'),
			('sess-pinned','fixture-project','read#6',6,'file_read','@nosuch/x.go'),
			('sess-pinned','fixture-project','read#7',7,'file_read',''),
			('sess-default','fixture-project','read#1',1,'file_read','notes.md')`, absSecondary)
	seed(`INSERT INTO evidence_index(id,project_id,source,hit_kind,session_id,handle,path) VALUES
			('idx-1','fixture-project','evidence','path','sess-pinned','read#2','@feature/src/lib.go'),
			('idx-2','fixture-project','evidence','path','sess-pinned','read#3','@main/README.md'),
			('idx-3','fixture-project','evidence','path','sess-pinned','read#5','/elsewhere/notes.txt')`)
}

func TestEvidenceRootIDBackfillKeysRecordsByRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := releasedRevision4Store(t, path)
	seedEvidenceRootFixture(t, source)
	testutil.FailErr(t, "close released source", source.Close())

	testutil.FailErr(t, "upgrade released source", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "restart upgraded store", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer func() { _ = upgraded.Close() }()

	type want struct {
		rootID string
		path   string
	}
	cases := map[string]want{
		"sess-pinned|read#1":  {rootID: "root-s", path: "src/app.go"},
		"sess-pinned|read#2":  {rootID: "root-s", path: "src/lib.go"},
		"sess-pinned|read#3":  {rootID: "root-p", path: "README.md"},
		"sess-pinned|read#4":  {rootID: "root-s", path: "docs/guide.md"},
		"sess-pinned|read#5":  {rootID: "", path: "/elsewhere/notes.txt"},
		"sess-pinned|read#6":  {rootID: "", path: "@nosuch/x.go"},
		"sess-pinned|read#7":  {rootID: "", path: ""},
		"sess-default|read#1": {rootID: "root-p", path: "notes.md"},
	}
	for key, want := range cases {
		session, handle, _ := strings.Cut(key, "|")
		var rootID, stored string
		testutil.FailErr(t, "read backfilled record "+key,
			upgraded.QueryRow(`SELECT root_id, path FROM evidence_records WHERE session_id=? AND handle=?`, session, handle).Scan(&rootID, &stored))
		if rootID != want.rootID || stored != want.path {
			t.Fatalf("%s backfilled to (%q, %q), want (%q, %q)", key, rootID, stored, want.rootID, want.path)
		}
	}

	// The derived index follows the rewritten labeled paths and leaves
	// legacy spellings alone.
	var indexed string
	testutil.FailErr(t, "read synced index path for read#2",
		upgraded.QueryRow(`SELECT path FROM evidence_index WHERE id='idx-1'`).Scan(&indexed))
	if indexed != "src/lib.go" {
		t.Fatalf("index path for read#2 = %q, want src/lib.go", indexed)
	}
	testutil.FailErr(t, "read synced index path for read#3",
		upgraded.QueryRow(`SELECT path FROM evidence_index WHERE id='idx-2'`).Scan(&indexed))
	if indexed != "README.md" {
		t.Fatalf("index path for read#3 = %q, want README.md", indexed)
	}
	testutil.FailErr(t, "read legacy index path for read#5",
		upgraded.QueryRow(`SELECT path FROM evidence_index WHERE id='idx-3'`).Scan(&indexed))
	if indexed != "/elsewhere/notes.txt" {
		t.Fatalf("index path for read#5 = %q", indexed)
	}

	// The seeded roots stay put so the resolved identities remain checkable.
	var count int
	testutil.FailErr(t, "count roots", upgraded.QueryRow(`SELECT COUNT(*) FROM project_roots WHERE project_id='fixture-project'`).Scan(&count))
	if count != 2 {
		t.Fatalf("project_roots rows = %d, want 2", count)
	}
}
