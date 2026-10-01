package app

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const draftJobID = "job-draft"

type draftFixture struct {
	drafts            presenceDrafts
	projects          *project.SQLRegistry
	documents         *editordoc.Service
	rootID, primary   string
	branch, projectID string
}

// newDraftFixture holds one primary root with primary files, and a completed
// worker for draftJobID whose branch started from them. A nil branch file is
// one the worker deleted.
func newDraftFixture(t *testing.T, primary, branch map[string][]byte) draftFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "presence-drafts.db")
	f := draftFixture{projectID: testdbseed.DefaultProjectID, rootID: uuid.NewString(), primary: t.TempDir(), branch: t.TempDir()}
	testdbseed.InsertProjectRootWithID(t, sqlDB, f.projectID, f.rootID, f.primary)
	ledger := sourceledger.New(sqlDB, t.TempDir())
	f.projects = project.NewSQLRegistry(sqlDB)
	f.documents = editordoc.New(editordoc.NewStore(sqlDB), ledger, f.projects)
	for path, content := range primary {
		writeFixtureFile(t, f.primary, path, string(content))
		writeFixtureFile(t, f.branch, path, string(content))
	}
	baseline := testbaseline.Capture(t, f.primary)
	for path, content := range branch {
		if content == nil {
			testutil.FailErr(t, "delete "+path, os.Remove(filepath.Join(f.branch, filepath.FromSlash(path))))
			continue
		}
		writeFixtureFile(t, f.branch, path, string(content))
	}
	task := &wire.WorkerTask{ID: draftJobID, ProjectID: f.projectID, WorkspaceRootID: f.rootID, WorkspacePath: f.primary,
		WorkspaceRoot: f.branch, WorkspaceBaselinePath: baseline, Scope: &wire.TaskScope{Mode: wire.TaskScopeModeWrite}}
	f.drafts = presenceDrafts{
		tasks: func(jobID string) (*wire.WorkerTask, bool) {
			if jobID != draftJobID {
				return nil, false
			}
			return task, true
		},
		projects: f.projects, documents: f.documents,
	}
	return f
}

func writeFixtureFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o750))
	testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte(content), 0o600))
}

func (f draftFixture) ready(t *testing.T, jobID string) map[string]agentpresence.DraftFile {
	t.Helper()
	files, err := f.drafts.ReadyFiles(t.Context(), jobID)
	testutil.FailErr(t, "ready files", err)
	byPath := make(map[string]agentpresence.DraftFile, len(files))
	for _, file := range files {
		if file.RootID != f.rootID {
			t.Fatalf("draft file %q in root %q, want %q", file.Path, file.RootID, f.rootID)
		}
		byPath[file.Path] = file
	}
	if len(byPath) != len(files) {
		t.Fatalf("draft files repeat a path: %+v", files)
	}
	return byPath
}

func count(n int) *int { return &n }

func TestPresenceDraftsReportChangedLinesOfTextFiles(t *testing.T) {
	f := newDraftFixture(t, map[string][]byte{
		"edit.txt": []byte("a\nb\nc\n"), "crlf.txt": []byte("one\ntwo\n"), "gone.txt": []byte("bye\n"),
		"blob.bin": []byte("text\n"), "same.txt": []byte("unchanged\n"),
	}, map[string][]byte{
		"edit.txt": []byte("a\nB\nc\nd\n"), "crlf.txt": []byte("one\r\ntwo\r\nthree\r\n"), "gone.txt": nil,
		"blob.bin": {0xff, 0xfe, 0x00, 0x01}, "new.txt": []byte("fresh\n"),
	})

	zero := 0
	target := func(path string) agentpresence.Target { return agentpresence.Target{RootID: f.rootID, Path: path} }
	want := map[string]agentpresence.DraftFile{
		"edit.txt": {Target: target("edit.txt"), Insertions: count(2), Deletions: count(1), Spans: []agentpresence.Span{
			{StartLine: 2, EndLine: 2},
			{StartLine: 4, EndLine: 4, StartCharacter: &zero, EndCharacter: &zero},
		}},
		"crlf.txt": {Target: target("crlf.txt"), Insertions: count(1), Deletions: count(0), Spans: []agentpresence.Span{
			{StartLine: 3, EndLine: 3, StartCharacter: &zero, EndCharacter: &zero},
		}},
		"gone.txt": {Target: target("gone.txt")},
		"blob.bin": {Target: target("blob.bin")},
		"new.txt": {Target: target("new.txt"), Insertions: count(1), Deletions: count(0), Spans: []agentpresence.Span{
			{StartLine: 1, EndLine: 1, StartCharacter: &zero, EndCharacter: &zero},
		}},
	}
	if got := f.ready(t, draftJobID); !reflect.DeepEqual(got, want) {
		t.Fatalf("draft files = %s\nwant %s", describeDrafts(got), describeDrafts(want))
	}
}

// The primary side is the text the person sees, so an open document's
// unsaved text replaces the file on disk.
func TestPresenceDraftsCompareAgainstTheOpenDocument(t *testing.T) {
	f := newDraftFixture(t, map[string][]byte{"open.txt": []byte("one\ntwo\n")}, map[string][]byte{"open.txt": []byte("one\ntwo\nthree\n")})
	p, err := f.projects.Get(t.Context(), f.projectID)
	testutil.FailErr(t, "load project", err)
	opened, err := f.documents.Open(t.Context(), p, "open.txt", f.rootID, "", "window", nil)
	testutil.FailErr(t, "open document", err)
	_, err = f.documents.ReplaceSnapshot(t.Context(), opened.ID, f.projectID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: opened.Revision},
		Content:         "one\ntwo\nthree\n", EOL: "lf",
	})
	testutil.FailErr(t, "type in document", err)

	want := map[string]agentpresence.DraftFile{
		"open.txt": {Target: agentpresence.Target{RootID: f.rootID, Path: "open.txt"}, Insertions: count(0), Deletions: count(0)},
	}
	if got := f.ready(t, draftJobID); !reflect.DeepEqual(got, want) {
		t.Fatalf("draft files = %s\nwant %s", describeDrafts(got), describeDrafts(want))
	}
}

func TestPresenceDraftsIgnoreUnknownJobs(t *testing.T) {
	f := newDraftFixture(t, map[string][]byte{"edit.txt": []byte("a\n")}, map[string][]byte{"edit.txt": []byte("b\n")})
	files, err := f.drafts.ReadyFiles(t.Context(), "job-unknown")
	testutil.FailErr(t, "ready files for unknown job", err)
	if files != nil {
		t.Fatalf("unknown job drafted %+v", files)
	}
}

func describeDrafts(files map[string]agentpresence.DraftFile) string {
	var b strings.Builder
	for _, path := range slices.Sorted(maps.Keys(files)) {
		file := files[path]
		fmt.Fprintf(&b, "%s:", path)
		if file.Insertions != nil && file.Deletions != nil {
			fmt.Fprintf(&b, " +%d -%d", *file.Insertions, *file.Deletions)
		}
		for _, span := range file.Spans {
			fmt.Fprintf(&b, " [%d-%d", span.StartLine, span.EndLine)
			if span.StartCharacter != nil && span.EndCharacter != nil {
				fmt.Fprintf(&b, " ch %d-%d", *span.StartCharacter, *span.EndCharacter)
			}
			b.WriteString("]")
		}
		b.WriteString("; ")
	}
	return b.String()
}
