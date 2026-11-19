package sourceledger

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func outsideRoot(t *testing.T, store *Store, ctx context.Context) string {
	t.Helper()
	id, err := store.queries.GetProjectOutsideRootID(ctx, "p1")
	testutil.FailErr(t, "read the outside namespace", err)
	return id
}

func insertChat(t *testing.T, store *Store, ctx context.Context, id, parent string) {
	t.Helper()
	var parentID any
	if parent != "" {
		parentID = parent
	}
	_, err := store.sqlDB.ExecContext(ctx, `INSERT INTO sessions (id,project_id,owner_person_id,posture,parent_session_id,created_at,activity_at,updated_at)
		VALUES (?,'p1',(SELECT id FROM people WHERE role = 'owner'),'build',?,'2026-09-13T00:00:00Z','2026-09-13T00:00:00Z','2026-09-13T00:00:00Z')`, id, parentID)
	testutil.FailErr(t, "create chat "+id, err)
}

func recordOutside(t *testing.T, store *Store, ctx context.Context, outside, session, path, after string) {
	t.Helper()
	testutil.FailErr(t, "record outside write "+path, store.Record(ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: outside, Path: path},
		ProjectID:      "p1", BranchID: sourcebranch.Trunk, SessionID: session,
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, After: []byte(after)}))
}

func TestOutsideWritesReferenceTheirRootChat(t *testing.T) {
	store, ctx := openLedger(t)
	outside := outsideRoot(t, store, ctx)
	insertChat(t, store, ctx, "chat", "")
	insertChat(t, store, ctx, "worker", "chat")
	recordOutside(t, store, ctx, outside, "worker", "home/me/.zshrc", "export A=1\n")
	recordOutside(t, store, ctx, outside, "worker", "home/me/.zshrc", "export A=2\n")
	testutil.FailErr(t, "record a project write", store.Record(ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "main.go"},
		ProjectID:      "p1", SessionID: "chat", Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("package main\n")}))

	refs, err := store.queries.ListSourceChatFiles(ctx, "chat")
	testutil.FailErr(t, "list chat references", err)
	if len(refs) != 1 || refs[0].Changed != 1 {
		t.Fatalf("chat references = %+v, want one changed outside file", refs)
	}
	workerRefs, err := store.queries.ListSourceChatFiles(ctx, "worker")
	testutil.FailErr(t, "list worker references", err)
	if len(workerRefs) != 0 {
		t.Fatalf("a worker held its own references: %+v", workerRefs)
	}
	head, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outside, "home/me/.zshrc")
	testutil.FailErr(t, "resolve the outside head", err)
	if head.FileID != refs[0].FileID {
		t.Fatalf("reference names %s, head is %s", refs[0].FileID, head.FileID)
	}
	held, err := store.OutsideFileHeld(ctx, "p1", "home/me/.zshrc")
	testutil.FailErr(t, "check the held file", err)
	if !held {
		t.Fatal("a referenced outside file reads as unheld")
	}
	held, err = store.OutsideFileHeld(ctx, "p1", "home/me/.bashrc")
	testutil.FailErr(t, "check an untouched file", err)
	if held {
		t.Fatal("an untouched outside path reads as held")
	}
}

func TestDeletingTheLastChatReleasesOutsideHistory(t *testing.T) {
	store, ctx := openLedger(t)
	outside := outsideRoot(t, store, ctx)
	insertChat(t, store, ctx, "first", "")
	insertChat(t, store, ctx, "second", "")
	recordOutside(t, store, ctx, outside, "first", "etc/shared.conf", "one\n")
	recordOutside(t, store, ctx, outside, "second", "etc/shared.conf", "two\n")
	recordOutside(t, store, ctx, outside, "first", "etc/only-first.conf", "first\n")

	deleteChat := func(id string) {
		tx, err := store.sqlDB.BeginTx(ctx, nil)
		testutil.FailErr(t, "begin delete "+id, err)
		_, err = db.DeleteSessionTree(ctx, tx, id)
		testutil.FailErr(t, "delete "+id, err)
		testutil.FailErr(t, "commit delete "+id, tx.Commit())
	}
	deleteChat("first")
	if _, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outside, "etc/only-first.conf"); err == nil {
		t.Fatal("a file no chat holds kept its history")
	}
	shared, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outside, "etc/shared.conf")
	testutil.FailErr(t, "keep the shared file", err)
	var versions int
	testutil.FailErr(t, "count shared versions", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_versions WHERE file_id = ?`, shared.FileID).Scan(&versions))
	if versions < 2 {
		t.Fatalf("the surviving chat lost the deleted chat's versions: %d", versions)
	}

	deleteChat("second")
	var files, orphans int
	testutil.FailErr(t, "count outside files", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_files WHERE id = ?`, shared.FileID).Scan(&files))
	testutil.FailErr(t, "count orphan operations", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_operations o WHERE o.project_id = 'p1'
		AND NOT EXISTS (SELECT 1 FROM source_effects e WHERE e.operation_id = o.id)
		AND NOT EXISTS (SELECT 1 FROM source_versions v WHERE v.operation_id = o.id)`).Scan(&orphans))
	if files != 0 || orphans != 0 {
		t.Fatalf("released history left %d files and %d operations", files, orphans)
	}
}

func TestAnOpenDocumentKeepsAnOutsideFile(t *testing.T) {
	store, ctx := openLedger(t)
	outside := outsideRoot(t, store, ctx)
	insertChat(t, store, ctx, "chat", "")
	recordOutside(t, store, ctx, outside, "chat", "srv/app.env", "KEY=1\n")
	head, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outside, "srv/app.env")
	testutil.FailErr(t, "resolve outside head", err)
	_, err = store.sqlDB.ExecContext(ctx, `INSERT INTO editor_documents (branch_id,id,project_id,file_id,root_id,path,draft,base_content,base_sha256,encoding,size_bytes,eol,base_eol,created_at,updated_at,opened_at)
		VALUES ('', 'doc', 'p1', ?, ?, 'srv/app.env', 'KEY=1', 'KEY=1', '', 'utf-8', 5, 'lf', 'lf', '2026-09-13T00:00:00Z', '2026-09-13T00:00:00Z', '2026-09-13T00:00:00Z')`, head.FileID, outside)
	testutil.FailErr(t, "open a document on the outside file", err)
	tx, err := store.sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin delete", err)
	_, err = db.DeleteSessionTree(ctx, tx, "chat")
	testutil.FailErr(t, "delete chat", err)
	testutil.FailErr(t, "commit delete", tx.Commit())
	if _, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outside, "srv/app.env"); err != nil {
		t.Fatalf("an open document lost its file history: %v", err)
	}
}

func TestAttachingAFolderAdoptsOutsideHistory(t *testing.T) {
	store, ctx := openLedger(t)
	outside := outsideRoot(t, store, ctx)
	insertChat(t, store, ctx, "chat", "")
	recordOutside(t, store, ctx, outside, "chat", "work/site/index.html", "<h1>one</h1>\n")
	recordOutside(t, store, ctx, outside, "chat", "work/site/css/app.css", "body{}\n")
	recordOutside(t, store, ctx, outside, "chat", "work/other.txt", "elsewhere\n")
	before, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outside, "work/site/css/app.css")
	testutil.FailErr(t, "resolve outside head", err)

	_, err = store.sqlDB.ExecContext(ctx, `INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind) VALUES ('site', 'p1', '/work/site', 'site', 0, '2026-09-13T00:00:00Z', 'attached')`)
	testutil.FailErr(t, "attach the folder", err)
	testutil.FailErr(t, "adopt outside files", db.AdoptOutsideTree(ctx, store.queries, "p1", "site", "work/site"))

	adopted, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "site", "css/app.css")
	testutil.FailErr(t, "resolve the adopted head", err)
	if adopted.FileID != before.FileID || adopted.VersionID != before.VersionID {
		t.Fatalf("adoption changed identity: %+v -> %+v", before, adopted)
	}
	if _, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outside, "work/site/index.html"); err == nil {
		t.Fatal("an adopted file is still addressed outside")
	}
	if _, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outside, "work/other.txt"); err != nil {
		t.Fatalf("a file beside the folder left the outside namespace: %v", err)
	}
	recordOutside(t, store, ctx, "site", "chat", "index.html", "<h1>two</h1>\n")
	index, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "site", "index.html")
	testutil.FailErr(t, "resolve the edited adopted file", err)
	var versions int
	testutil.FailErr(t, "count versions", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_versions WHERE file_id = ?`, index.FileID).Scan(&versions))
	if versions < 2 {
		t.Fatalf("a write after adoption started a new history: %d versions", versions)
	}
}
