package native

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// An operation on a path saves the person's drafts there and beneath it, and
// leaves drafts elsewhere unsaved.
func TestSaveDraftsSavesDraftsAtAndBeneathThePath(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "directory", os.Mkdir(filepath.Join(dir, "dir"), 0o755))
	for _, name := range []string{"dir/a.txt", "dir/b.txt", "other.txt"} {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte("saved\n"), 0o644))
	}
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{
		"r1/dir/a.txt": openDoc("doc-a", "typed a\n", true),
		"r1/dir/b.txt": openDoc("doc-b", "typed b\n", true),
		"r1/other.txt": openDoc("doc-other", "typed other\n", true),
	}}
	revisions := ExecutionRevisions{Boundary: nativefixture.Boundary(t)}
	tctx := editorCtx(dir, docs)
	testutil.FailErr(t, "save one path", revisions.SaveDrafts(t.Context(), tctx, filepath.Join(dir, "dir", "a.txt"), false))
	if docs.docs["r1/dir/a.txt"].Dirty || !docs.docs["r1/dir/b.txt"].Dirty {
		t.Fatalf("one path saved %+v and %+v", docs.docs["r1/dir/a.txt"], docs.docs["r1/dir/b.txt"])
	}
	testutil.FailErr(t, "save subtree", revisions.SaveDrafts(t.Context(), tctx, filepath.Join(dir, "dir"), true))
	if docs.docs["r1/dir/b.txt"].Dirty || !docs.docs["r1/other.txt"].Dirty {
		t.Fatalf("subtree saved %+v; outside it %+v", docs.docs["r1/dir/b.txt"], docs.docs["r1/other.txt"])
	}
}

// A draft held over a file that changed outside it is a revision conflict that
// names both revisions.
func TestSaveDraftsRefusesAFileChangedOutsideItsDocument(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("outside\n"), 0o644))
	doc := openDoc("doc-a", "typed\n", true)
	doc.Diverged, doc.BaseSHA256 = true, strings.Repeat("b", 64)
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.txt": doc}}
	revisions := ExecutionRevisions{Boundary: nativefixture.Boundary(t)}
	err := revisions.SaveDrafts(t.Context(), editorCtx(dir, docs), filepath.Join(dir, "a.txt"), false)
	var conflict *ExecutionRevisionConflict
	if !errors.As(err, &conflict) || conflict.Pinned.DiskSHA256 != doc.BaseSHA256 || conflict.Current.Mode != toolcontract.AbsentRevision {
		t.Fatalf("err=%v", err)
	}
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "COMMAND_REVISION_CONFLICT" || reject.Data["expected_revision"] == nil || reject.Data["observed_revision"] == nil {
		t.Fatalf("reject=%+v", reject)
	}
	if !docs.docs["r1/a.txt"].Dirty {
		t.Fatal("the draft was saved over the outside change")
	}
}
