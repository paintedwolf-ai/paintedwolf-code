//go:build darwin || linux

package native

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestSaveDraftsSavesOrRejectsAGrantedOutsideDescendant(t *testing.T) {
	_, tc, _, outside := outsideNativeFixture(t)
	dir := fspath.CanonicalPath(outside)
	file := filepath.Join(dir, "draft.txt")
	testutil.FailErr(t, "seed outside file", os.WriteFile(file, []byte("published\n"), 0o600))
	rel, ok := projectroot.OutsideRel(file)
	if !ok {
		t.Fatal("fixture has no outside address")
	}
	draft := openDoc("outside-editor", "unsaved\n", true)
	draft.Diverged, draft.BaseSHA256 = true, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tc.Source.EditorDocuments = &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{tc.Source.OutsideRoot.ID + "/" + rel: draft}}
	tc.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: dir, Write: true}, {Path: file, Write: true}}
	revisions := ExecutionRevisions{Boundary: nativefixture.Boundary(t)}

	testutil.FailErr(t, "save just the outside folder", revisions.SaveDrafts(t.Context(), tc, dir, false))
	for _, operand := range []string{dir, file} {
		err := revisions.SaveDrafts(t.Context(), tc, operand, operand == dir)
		var conflict *ExecutionRevisionConflict
		if !errors.As(err, &conflict) || conflict.Path != file || conflict.Pinned.DiskSHA256 != draft.BaseSHA256 || conflict.Current.Mode != toolcontract.AbsentRevision {
			t.Fatalf("draft save for %s = %+v, %v", operand, conflict, err)
		}
	}
}
