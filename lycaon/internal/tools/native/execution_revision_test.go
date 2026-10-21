package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

type pinnedDiskFixture struct {
	dir       string
	tctx      tools.ToolContext
	revisions ExecutionRevisions
	write     *WriteTool
}

func newPinnedDiskFixture(t *testing.T, files map[string]string) pinnedDiskFixture {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	boundary := nativefixture.Boundary(t)
	return pinnedDiskFixture{dir: dir, tctx: nativefixture.Context(dir), revisions: ExecutionRevisions{Boundary: boundary}, write: &WriteTool{Boundary: boundary}}
}

func (f pinnedDiskFixture) disk(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, name))
	testutil.FailErr(t, "read "+name, err)
	return string(raw)
}

func requireRejectCode(t *testing.T, err error, code string) {
	t.Helper()
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestExecutionRevisionsCaptureDiskOutsideDocumentOwner(t *testing.T) {
	f := newPinnedDiskFixture(t, map[string]string{"a.txt": "one\r\ntwo\r\n"})
	if err := os.Mkdir(filepath.Join(f.dir, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := f.revisions.CaptureSource(t.Context(), f.tctx, "a.txt")
	testutil.FailErr(t, "capture source", err)
	raw := textfile.SHA256([]byte("one\r\ntwo\r\n"))
	want := toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision, RootID: "r1", Path: "a.txt",
		DiskSHA256: raw, Representation: toolcontract.UTF8SourceBytes, ContentSHA256: textfile.SHA256([]byte(source.Content)), ContentBytes: int64(len(source.Content))}
	if source.Revision != want || source.Path != "a.txt" {
		t.Fatalf("source = %+v", source)
	}
	declared, err := f.revisions.DeclareLaunchInput(t.Context(), f.tctx, "a.txt")
	testutil.FailErr(t, "declare launch input", err)
	if declared.Basis != toolcontract.DeclaredAtLaunch || declared.DiskSHA256 != raw || declared.ContentBytes != 10 || declared.Validate() != nil {
		t.Fatalf("declared = %+v", declared)
	}
	for _, path := range []string{"missing.txt", "dir"} {
		_, err := f.revisions.DeclareLaunchInput(t.Context(), f.tctx, path)
		requireRejectCode(t, err, "READ_PATH_NOT_FOUND")
	}
	_, err = f.revisions.CaptureSource(t.Context(), f.tctx, "missing.txt")
	if err == nil {
		t.Fatal("missing source captured")
	}
	testutil.FailErr(t, "unchanged pin verifies", f.revisions.Verify(t.Context(), f.tctx, source.Revision))
	stray := source.Revision
	stray.RootID = "detached-root"
	var conflict *ExecutionRevisionConflict
	if err := f.revisions.Verify(t.Context(), f.tctx, stray); !errors.As(err, &conflict) {
		t.Fatalf("detached root verified: %v", err)
	}
	testutil.FailErr(t, "external edit", os.WriteFile(filepath.Join(f.dir, "a.txt"), []byte("moved\n"), 0o644))
	err = f.revisions.Verify(t.Context(), f.tctx, source.Revision)
	if !errors.As(err, &conflict) || conflict.Current.DiskSHA256 != textfile.SHA256([]byte("moved\n")) || conflict.Current.Same(source.Revision) {
		t.Fatalf("moved pin: %v", err)
	}
	requireRejectCode(t, err, "COMMAND_REVISION_CONFLICT")
	if conflict.Error() == "" {
		t.Fatal("conflict has no diagnostic")
	}
}

func TestPinnedWriteRefusesMalformedAdmission(t *testing.T) {
	f := newPinnedDiskFixture(t, map[string]string{"a.txt": "base\n", "b.txt": "other\n"})
	destination, err := f.revisions.CaptureDestination(t.Context(), f.tctx, "a.txt")
	testutil.FailErr(t, "capture destination", err)
	source, err := f.revisions.CaptureSource(t.Context(), f.tctx, "b.txt")
	testutil.FailErr(t, "capture source", err)
	declared, err := f.revisions.DeclareLaunchInput(t.Context(), f.tctx, "b.txt")
	testutil.FailErr(t, "declare input", err)
	cases := map[string]WriteInput{
		"unknown admission":       {Path: "a.txt", Content: "x\n", Admission: WriteAdmission(9)},
		"missing destination":     {Path: "a.txt", Content: "x\n", Admission: WriteExecutionPinned},
		"source as destination":   {Path: "b.txt", Content: "x\n", Admission: WriteExecutionPinned, Destination: source.Revision},
		"other path":              {Path: "b.txt", Content: "x\n", Admission: WriteExecutionPinned, Destination: destination},
		"declared source":         {Path: "a.txt", Content: "x\n", Admission: WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{declared}},
		"destination as a source": {Path: "a.txt", Content: "x\n", Admission: WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{destination}},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.write.Apply(t.Context(), input, f.tctx)
			requireRejectCode(t, err, "COMMAND_NATIVE_FORM_UNSUPPORTED")
			if f.disk(t, "a.txt") != "base\n" || f.disk(t, "b.txt") != "other\n" {
				t.Fatal("refused admission published")
			}
		})
	}
	if _, err := f.write.Apply(t.Context(), WriteInput{Admission: WriteExecutionPinned}, f.tctx); err == nil {
		t.Fatal("pinned write without a path")
	}
}

func TestPinnedDiskWriteConflictsAfterReview(t *testing.T) {
	cases := map[string]struct {
		move string
		role toolcontract.ExecutionRevisionRole
	}{
		"destination":   {move: "a.txt", role: toolcontract.DestinationRevision},
		"pinned source": {move: "b.txt", role: toolcontract.SourceRevision},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPinnedDiskFixture(t, map[string]string{"a.txt": "base\n", "b.txt": "input\n"})
			source, err := f.revisions.CaptureSource(t.Context(), f.tctx, "b.txt")
			testutil.FailErr(t, "capture source", err)
			destination, err := f.revisions.CaptureDestination(t.Context(), f.tctx, "a.txt")
			testutil.FailErr(t, "capture destination", err)
			reviews := 0
			f.tctx.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
				reviews++
				return os.WriteFile(filepath.Join(f.dir, tc.move), []byte("concurrent\n"), 0o644)
			}
			_, err = f.write.Apply(t.Context(), WriteInput{Path: "a.txt", Content: "INPUT\n", Admission: WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{source.Revision}}, f.tctx)
			var conflict *ExecutionRevisionConflict
			if !errors.As(err, &conflict) || conflict.Pinned.Role != tc.role || reviews != 1 {
				t.Fatalf("reviews=%d err=%v", reviews, err)
			}
			requireRejectCode(t, err, "COMMAND_REVISION_CONFLICT")
			if f.disk(t, "a.txt") == "INPUT\n" {
				t.Fatal("stale transform published")
			}
		})
	}
}

func TestPinnedDiskAppendExtendsCapturedDestination(t *testing.T) {
	f := newPinnedDiskFixture(t, map[string]string{"log.txt": "one\n"})
	destination, err := f.revisions.CaptureDestination(t.Context(), f.tctx, "log.txt")
	testutil.FailErr(t, "capture destination", err)
	result, err := f.write.Apply(t.Context(), WriteInput{Path: "log.txt", Content: "two\n", Append: true, Admission: WriteExecutionPinned, Destination: destination}, f.tctx)
	testutil.FailErr(t, "append", err)
	if f.disk(t, "log.txt") != "one\ntwo\n" || !result.Saved || result.PublishedSHA256 != textfile.SHA256([]byte("one\ntwo\n")) {
		t.Fatalf("result=%+v disk=%q", result, f.disk(t, "log.txt"))
	}
	absent, err := f.revisions.CaptureDestination(t.Context(), f.tctx, "new.txt")
	testutil.FailErr(t, "capture absent", err)
	_, err = f.write.Apply(t.Context(), WriteInput{Path: "new.txt", Content: "x\n", Append: true, Admission: WriteExecutionPinned, Destination: absent}, f.tctx)
	requireRejectCode(t, err, "READ_PATH_NOT_FOUND")
}

func TestPublicationBarrierTracksOnlyUnpublishedAcceptedWrites(t *testing.T) {
	var barrier PublicationBarrier
	tctx := nativefixture.Context(t.TempDir())
	testutil.FailErr(t, "admit with nothing pending", barrier.AdmitDependentLaunch(t.Context(), tctx))
	testutil.FailErr(t, "saved write", barrier.Track(WriteResult{Path: "a.txt", Accepted: true, Saved: true}))
	testutil.FailErr(t, "refused write", barrier.Track(WriteResult{Path: "b.txt"}))
	testutil.FailErr(t, "published writes do not block", barrier.AdmitDependentLaunch(t.Context(), tctx))
	if err := barrier.Track(WriteResult{Path: "c.txt", Accepted: true}); err == nil {
		t.Fatal("unpublished write without document identity tracked")
	}
	testutil.FailErr(t, "accepted document write", barrier.Track(WriteResult{Path: "d.txt", Accepted: true, DocumentID: "doc-d", AcceptedRevision: 3}))
	err := barrier.AdmitDependentLaunch(t.Context(), tctx)
	var pending *PublicationPending
	if !errors.As(err, &pending) || len(pending.Paths) != 1 || pending.Paths[0] != "d.txt" || pending.Error() == "" {
		t.Fatalf("launch without document owner: %v", err)
	}
	requireRejectCode(t, err, "COMMAND_PUBLICATION_PENDING")
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{}}
	tctx = editorCtx(t.TempDir(), docs)
	if err := barrier.AdmitDependentLaunch(t.Context(), tctx); err == nil || errors.As(err, &pending) {
		t.Fatalf("owner failure must not read as a publication state: %v", err)
	}
}
