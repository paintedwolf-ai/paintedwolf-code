package sourcecatalog

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func treeTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	c := New()
	c.treeDir = t.TempDir()
	t.Cleanup(func() { testutil.FailErr(t, "drain tree indexing", c.Drain(context.Background())) })
	return c
}

func readySummary(t *testing.T, c *Catalog, root Root, scope TreeScope) *SummaryReader {
	t.Helper()
	r, status, err := c.OpenSummary(t.Context(), "project", root, scope, 30*time.Second)
	testutil.FailErr(t, "open summary tree", err)
	if r == nil || status.State != StateReady || !status.Complete {
		t.Fatalf("summary status=%+v", status)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// readyIndex joins the file index until its discovery has covered the tree.
func readyIndex(t *testing.T, c *Catalog, root Root) *IndexReader {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r, status, err := c.OpenIndex(t.Context(), "project", root, 5*time.Second)
		testutil.FailErr(t, "open file index", err)
		if r != nil && status.Complete {
			t.Cleanup(func() { _ = r.Close() })
			return r
		}
		_ = r.Close()
		if time.Now().After(deadline) {
			t.Fatalf("index status=%+v", status)
		}
	}
}

// summaryStoreFor resolves the same store OpenSummary would, so a test can
// drive one reconciliation pass directly.
func summaryStoreFor(t *testing.T, c *Catalog, root Root, scope TreeScope) *summaryStore {
	t.Helper()
	s, err := c.summaryStore(t.Context(), "project", root, scope)
	testutil.FailErr(t, "summary store", err)
	return s
}

func summaryReaderFor(t *testing.T, s *summaryStore) *SummaryReader {
	t.Helper()
	db, tx, status, err := s.readTx(t.Context(), TreeStatus{State: StateReady})
	testutil.FailErr(t, "reopen generation", err)
	return &SummaryReader{db: db, tx: tx, Status: status}
}

func writeTreeTestFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	testutil.FailErr(t, "create parent", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write source", os.WriteFile(abs, []byte(body), 0o644))
}

func TestTreePagesPreserveTotalsAndVisitEveryChild(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	for i := range 137 {
		writeTreeTestFile(t, root.Path, fmt.Sprintf("file-%03d.txt", i), "material")
	}
	writeTreeTestFile(t, root.Path, "zebra/auth/permissions.go", "authorization")
	r := readySummary(t, c, root, TreeScope{Key: "all"})
	parent, err := r.Node(t.Context(), ".")
	testutil.FailErr(t, "root aggregate", err)
	if parent.Files != 138 || parent.Children != 138 {
		t.Fatalf("root=%+v", parent)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		before := r.RowsRead
		page, pageErr := r.Page(t.Context(), parent, cursor, "permissions", nil, 12)
		testutil.FailErr(t, "page", pageErr)
		if r.RowsRead-before > 32 {
			t.Fatalf("page visited %d rows", r.RowsRead-before)
		}
		if cursor == "" && page.Nodes[0].Path != "zebra" {
			t.Fatalf("task branch not promoted: %+v", page.Nodes)
		}
		for _, n := range page.Nodes {
			if seen[n.Path] {
				t.Fatalf("repeated child %s", n.Path)
			}
			seen[n.Path] = true
		}
		if page.RemainingChildren != parent.Children-len(seen) {
			t.Fatalf("remaining=%d seen=%d", page.RemainingChildren, len(seen))
		}
		cursor = page.Next
		if cursor == "" {
			break
		}
	}
	if len(seen) != parent.Children {
		t.Fatalf("visited %d children, want %d", len(seen), parent.Children)
	}
}

func TestTreeIncrementalUpdatePinsOldGeneration(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a/b/file.txt", "old")
	writeTreeTestFile(t, root.Path, "sibling.txt", "same")
	scope := TreeScope{Key: "all"}
	old := readySummary(t, c, root, scope)
	before, err := old.Node(t.Context(), ".")
	testutil.FailErr(t, "old totals", err)
	writeTreeTestFile(t, root.Path, "a/b/file.txt", "new material")
	c.InvalidateRoot(root.Path, "a/b/file.txt")
	fresh := readySummary(t, c, root, scope)
	after, err := fresh.Node(t.Context(), ".")
	testutil.FailErr(t, "new totals", err)
	pinned, err := old.Node(t.Context(), ".")
	testutil.FailErr(t, "pinned totals", err)
	if after.Files != 2 || after.Bytes != 16 || pinned.Bytes != before.Bytes || old.Status.Revision == fresh.Status.Revision {
		t.Fatalf("before=%+v after=%+v pinned=%+v", before, after, pinned)
	}
}

func TestTreeIncrementalMatchesFullAfterNamespaceChanges(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "removed/old.txt", "old")
	writeTreeTestFile(t, root.Path, "dir/old.txt", "old")
	scope := TreeScope{Key: "all"}
	_ = readySummary(t, c, root, scope)
	testutil.FailErr(t, "remove subtree", os.RemoveAll(filepath.Join(root.Path, "removed")))
	testutil.FailErr(t, "remove replaced directory", os.RemoveAll(filepath.Join(root.Path, "dir")))
	outside := t.TempDir()
	writeTreeTestFile(t, outside, "private.txt", "private")
	testutil.FailErr(t, "replace directory with symlink", os.Symlink(outside, filepath.Join(root.Path, "dir")))
	writeTreeTestFile(t, root.Path, "new/deep/first.txt", "first")
	writeTreeTestFile(t, root.Path, "new/deep/second.txt", "second")
	c.InvalidateRoot(root.Path, "removed/old.txt", "dir/private.txt", "new/deep/first.txt")
	r := readySummary(t, c, root, scope)
	n, err := r.Node(t.Context(), ".")
	testutil.FailErr(t, "updated root", err)
	if n.Files != 2 || n.Bytes != 11 || n.Children != 2 {
		t.Fatalf("updated root=%+v", n)
	}
	if _, err = r.Node(t.Context(), "dir/private.txt"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("symlink descendant error=%v", err)
	}
}

func TestTreeReadScopesNeverShareCounts(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "public.txt", "public")
	writeTreeTestFile(t, root.Path, "secret.txt", "secret")
	r := readySummary(t, c, root, TreeScope{Key: "public", Filter: func(rel string, _ bool) bool { return rel == "public.txt" }})
	n, err := r.Node(t.Context(), ".")
	testutil.FailErr(t, "restricted aggregate", err)
	if n.Files != 1 {
		t.Fatalf("restricted root=%+v", n)
	}
	all := readySummary(t, c, root, TreeScope{Key: "all"})
	n, err = all.Node(t.Context(), ".")
	testutil.FailErr(t, "unrestricted aggregate", err)
	if n.Files != 2 {
		t.Fatalf("unrestricted root=%+v", n)
	}
}

func TestTreeWidePageAndEditWorkDoesNotGrowWithRepository(t *testing.T) {
	for _, size := range []int{1000, 4096} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			assertTreeWidePageAndEditWork(t, size)
		})
	}
}

func assertTreeWidePageAndEditWork(t *testing.T, size int) {
	t.Helper()
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	s := summaryStoreFor(t, c, root, TreeScope{Key: "all"})
	testutil.FailErr(t, "initialize tree", loadStore(t.Context(), s))
	db, err := openTreeDB(t.Context(), s.file)
	testutil.FailErr(t, "open tree database", err)
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "seed transaction", err)
	w, err := newSummaryWriter(t.Context(), tx, s)
	testutil.FailErr(t, "seed writer", err)
	seedWideTree(t, tx, size)
	testutil.FailErr(t, "seed root", w.write(t.Context(), TreeNode{Entry: Entry{Path: ".", IsDir: true}, Files: size, Bytes: int64(size), Children: size}))
	_, err = tx.ExecContext(t.Context(), "INSERT INTO meta VALUES(1,1,?,1)", time.Now().UnixNano())
	testutil.FailErr(t, "seed revision", err)
	w.close()
	testutil.FailErr(t, "commit seed", tx.Commit())
	r := summaryReaderFor(t, s)
	defer func() { _ = r.Close() }()
	parent, err := r.Node(t.Context(), ".")
	testutil.FailErr(t, "root", err)
	page, err := r.Page(t.Context(), parent, "", "", nil, 24)
	testutil.FailErr(t, "wide page", err)
	if r.RowsRead != 26 || len(page.Nodes) != 24 || page.RemainingFiles != size-24 {
		t.Fatalf("rows=%d page=%+v", r.RowsRead, page)
	}
	planRows, err := r.tx.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+summaryFilesPageQuery, "file-000000050", "\U0010ffff", 25)
	testutil.FailErr(t, "file page query plan", err)
	defer func() { _ = planRows.Close() }()
	indexed := false
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		testutil.FailErr(t, "read query plan", planRows.Scan(&id, &parent, &unused, &detail))
		indexed = indexed || strings.Contains(detail, "source_page") && strings.Contains(detail, "SEARCH")
	}
	testutil.FailErr(t, "query plan rows", planRows.Err())
	if !indexed {
		t.Fatal("file page must seek the partial source index")
	}
	for _, after := range []string{"", fmt.Sprintf("file-%09d", size-50)} {
		before := r.RowsRead
		files, more, err := r.FilesPage(t.Context(), ".", after, 24)
		testutil.FailErr(t, "bounded file seek", err)
		if r.RowsRead-before != 25 || len(files) != 24 || !more {
			t.Fatalf("file seek rows=%d files=%d more=%v", r.RowsRead-before, len(files), more)
		}
	}
	assertTreeLatePageWork(t, r, parent, page.Next, size)
	writeTreeTestFile(t, root.Path, "file-000000000", "new bytes")
	assertTreeUpdateWork(t, db, s, "file-000000000", 2)
}

func assertTreeLatePageWork(t *testing.T, r *SummaryReader, parent TreeNode, cursor string, size int) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	testutil.FailErr(t, "decode position", err)
	var position treePosition
	testutil.FailErr(t, "read position", json.Unmarshal(raw, &position))
	position.After = treeRank(TreeNode{Entry: Entry{Path: fmt.Sprintf("file-%09d", size-50)}, Files: 1, Bytes: 1})
	position.Seen, position.Files, position.Bytes = size-49, size-49, int64(size-49)
	raw, err = json.Marshal(position)
	testutil.FailErr(t, "encode late position", err)
	before := r.RowsRead
	page, err := r.Page(t.Context(), parent, base64.RawURLEncoding.EncodeToString(raw), "", nil, 24)
	testutil.FailErr(t, "late page", err)
	if r.RowsRead-before != 25 || len(page.Nodes) != 24 || page.RemainingFiles != 25 || page.Nodes[0].Path != fmt.Sprintf("file-%09d", size-49) {
		t.Fatalf("late page rows=%d remaining=%d nodes=%d", r.RowsRead-before, page.RemainingFiles, len(page.Nodes))
	}
}

func seedWideTree(t *testing.T, tx *sql.Tx, size int) {
	t.Helper()
	_, err := tx.ExecContext(t.Context(), `
WITH RECURSIVE sequence(i) AS (VALUES(0) UNION ALL SELECT i+1 FROM sequence WHERE i+1 < ?),
numbered(path) AS (SELECT printf('file-%09d',i) FROM sequence)
INSERT INTO nodes
SELECT path,'.','file',1,0,0,0,1,0,0,1,1,0,path,? || path,0,0 FROM numbered`, size, treeRank(TreeNode{Files: 1, Bytes: 1}))
	testutil.FailErr(t, "seed wide nodes", err)
	_, err = tx.ExecContext(t.Context(), "INSERT INTO terms SELECT 'file',path FROM nodes")
	testutil.FailErr(t, "seed wide terms", err)
}

func assertTreeUpdateWork(t *testing.T, db *sql.DB, s *summaryStore, rel string, want int) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	testutil.FailErr(t, "update transaction", err)
	defer func() { _ = tx.Rollback() }()
	w, err := newSummaryWriter(t.Context(), tx, s)
	testutil.FailErr(t, "update writer", err)
	defer w.close()
	testutil.FailErr(t, "apply one edit", w.updatePaths(t.Context(), []string{rel}))
	if w.RowsWritten != want {
		t.Fatalf("edit wrote %d rows, want %d", w.RowsWritten, want)
	}
}
