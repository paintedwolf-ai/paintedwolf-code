package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// fakeEditorDocuments is the open-document view a test controls: which
// paths are open, what they hold, and how an apply answers.
type fakeEditorDocuments struct {
	reads map[string]tools.EditorDocumentText
	docs  map[string]*tools.EditorDocumentText // rootID/path
	// applied records every apply the tools asked for.
	applied []tools.EditorDocumentEdit
	batches [][]tools.EditorDocumentEdit
	// moveOnce makes the first apply report a moved document and advance
	// the text, the way a keystroke between read and apply does.
	moveOnce func(doc *tools.EditorDocumentText)
	// unsaved answers applies as held by the document but not saved.
	unsaved bool
	// alwaysMoved answers every apply as moved.
	alwaysMoved bool
	openErr     error
	dirtyErr    error
}

func (f *fakeEditorDocuments) RememberAgentRead(_ string, sessionID string, document tools.EditorDocumentText) {
	if f.reads == nil {
		f.reads = make(map[string]tools.EditorDocumentText)
	}
	f.reads[sessionID+"/"+document.ID] = document
}
func (f *fakeEditorDocuments) AgentReadBase(_ context.Context, _ string, sessionID, documentID string, frozen *tools.AgentReadBases) (tools.EditorDocumentText, error) {
	document, ok := f.reads[sessionID+"/"+documentID]
	if !ok {
		return tools.EditorDocumentText{}, tools.ErrEditorReadRequired
	}
	if frozen != nil {
		if revision, ok := frozen.Lookup(documentID); !ok || revision != document.Revision {
			return tools.EditorDocumentText{}, tools.ErrEditorReadRequired
		}
	}
	return document, nil
}

// AdvanceAgentRead mirrors the adapter: the agent's own complete text becomes
// the basis; a result holding anyone else's text withdraws it.
func (f *fakeEditorDocuments) AdvanceAgentRead(projectID, sessionID string, frozen *tools.AgentReadBases, edit tools.EditorDocumentEdit, document tools.EditorDocumentText) {
	if document.Text == strings.ReplaceAll(edit.Content, "\r\n", "\n") {
		f.RememberAgentRead(projectID, sessionID, document)
		frozen.Advance(document.ID, document.Revision)
		return
	}
	delete(f.reads, sessionID+"/"+document.ID)
	frozen.Forget(document.ID)
}

func (f *fakeEditorDocuments) OpenDocument(_ context.Context, _ string, _ sourcebranch.ID, rootID, path string) (tools.EditorDocumentText, bool, error) {
	if f.openErr != nil {
		return tools.EditorDocumentText{}, false, f.openErr
	}
	doc, ok := f.docs[rootID+"/"+path]
	if !ok {
		return tools.EditorDocumentText{}, false, nil
	}
	return *doc, true, nil
}

func (f *fakeEditorDocuments) DirtyDocuments(_ context.Context, _ string, _ sourcebranch.ID) ([]tools.EditorDocumentPath, error) {
	if f.dirtyErr != nil {
		return nil, f.dirtyErr
	}
	var out []tools.EditorDocumentPath
	for key, doc := range f.docs {
		if !doc.Dirty {
			continue
		}
		rootID, path, _ := strings.Cut(key, "/")
		out = append(out, tools.EditorDocumentPath{RootID: rootID, Path: path, Text: doc.Text})
	}
	return out, nil
}

func (f *fakeEditorDocuments) ApplyAgentEdit(_ context.Context, _ string, edit tools.EditorDocumentEdit) (tools.EditorDocumentApplied, error) {
	f.applied = append(f.applied, edit)
	var doc *tools.EditorDocumentText
	for _, candidate := range f.docs {
		if candidate.ID == edit.DocumentID {
			doc = candidate
		}
	}
	if doc == nil {
		return tools.EditorDocumentApplied{}, errors.New("unknown document")
	}
	if f.alwaysMoved {
		return tools.EditorDocumentApplied{}, tools.ErrEditorDocumentMoved
	}
	if f.moveOnce != nil {
		move := f.moveOnce
		f.moveOnce = nil
		move(doc)
		return tools.EditorDocumentApplied{}, tools.ErrEditorDocumentMoved
	}
	if doc.Revision != edit.ExpectedRevision {
		return tools.EditorDocumentApplied{}, tools.ErrEditorDocumentMoved
	}
	doc.Text = edit.Content
	doc.Revision++
	doc.Dirty = f.unsaved
	return tools.EditorDocumentApplied{Document: *doc, Saved: !f.unsaved}, nil
}

func editorCtx(dir string, documents tools.EditorDocuments) tools.ToolContext {
	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID = "p1"
	tctx.Identity.ToolCallID = "call-7"
	tctx.Identity.UserTurn = 3
	tctx.Source.EditorDocuments = documents
	// These tool fixtures begin with a prior agent read of each declared document.
	if fake, ok := documents.(*fakeEditorDocuments); ok {
		for _, document := range fake.docs {
			fake.RememberAgentRead(tctx.Identity.ProjectID, tctx.Identity.SessionID, *document)
		}
	}
	return tctx
}

func openDoc(id, text string, dirty bool) *tools.EditorDocumentText {
	return &tools.EditorDocumentText{ID: id, Revision: 4, Text: text, SHA256: strings.Repeat("d", 64), Dirty: dirty}
}

func readContent(t *testing.T, out string) surveytools.ReadResponse {
	t.Helper()
	var resp surveytools.ReadResponse
	testutil.FailErr(t, "decode read", json.Unmarshal([]byte(out), &resp))
	return resp
}

// A file the person has open reads as their document, and the receipt says
// the text is their unsaved draft rather than a recorded state.
func TestReadServesTheOpenDocument(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package disk\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.go": openDoc("doc-a", "package draft\n", true)}}
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := read.Run(context.Background(), map[string]any{"path": "a.go"}, editorCtx(dir, docs))
	testutil.FailErr(t, "read", err)
	if resp := readContent(t, out); resp.Content != "     1: package draft" {
		t.Fatalf("content = %q, want the editor draft", resp.Content)
	}
	source := receiptSource(t, out)
	editor, _ := source["editor"].(map[string]any)
	if editor == nil || editor["dirty"] != true || editor["revision"] != float64(4) {
		t.Fatalf("editor stamp = %v", source)
	}
	if source["recorded"] != false || source["note"] != sourceview.EditorDraftNote {
		t.Fatalf("draft stamp = %v, want unrecorded with the editor note", source)
	}
}

// A clean document is the file; the receipt still says the editor served it.
func TestReadStampsACleanDocumentAsEditorServed(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.go": openDoc("doc-a", "package a\n", false)}}
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := read.Run(context.Background(), map[string]any{"path": "a.go"}, editorCtx(dir, docs))
	testutil.FailErr(t, "read", err)
	source := receiptSource(t, out)
	editor, _ := source["editor"].(map[string]any)
	if editor == nil || editor["dirty"] != false || source["note"] == sourceview.EditorDraftNote {
		t.Fatalf("clean document stamp = %v", source)
	}
}

// Ranged and symbol reads serve the same text as a whole read.
func TestReadRangesServeTheOpenDocument(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("disk one\ndisk two\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.txt": openDoc("doc-a", "draft one\ndraft two\n", true)}}
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := read.Run(context.Background(), map[string]any{
		"path": "a.txt", "ranges": []any{map[string]any{"offset": 2, "limit": 1}},
	}, editorCtx(dir, docs))
	testutil.FailErr(t, "read ranges", err)
	if !strings.Contains(out, "draft two") || strings.Contains(out, "disk two") {
		t.Fatalf("ranged read = %s", out)
	}
	if editor, _ := receiptSource(t, out)["editor"].(map[string]any); editor == nil {
		t.Fatalf("ranged read lost the editor stamp: %s", out)
	}
}

// An edit to an open file lands in the document, not on disk, and the
// receipt states where it went.
func TestEditLandsInTheOpenDocument(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package disk\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.go": openDoc("doc-a", "package draft\n", true)}}
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}
	out, err := edit.Run(context.Background(), map[string]any{
		"path": "a.go", "old_string": "draft", "new_string": "agent",
	}, editorCtx(dir, docs))
	testutil.FailErr(t, "edit", err)
	if len(docs.applied) != 1 {
		t.Fatalf("applies = %d, want 1", len(docs.applied))
	}
	applied := docs.applied[0]
	if applied.DocumentID != "doc-a" || applied.ExpectedRevision != 4 || applied.Content != "package agent\n" ||
		applied.ToolName != "edit" || applied.ToolCallID != "call-7" || applied.Turn != 3 || applied.OperationID == "" {
		t.Fatalf("apply = %+v", applied)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "a.go"))
	testutil.FailErr(t, "read disk", err)
	if string(raw) != "package disk\n" {
		t.Fatalf("edit wrote disk directly: %q", raw)
	}
	if !strings.Contains(out, "shared editor document and saved") {
		t.Fatalf("receipt = %q, want the editor landing stated", out)
	}
}

// An edit against an old_string the person already changed on disk but not
// in the editor is judged against the editor: that is what they see.
func TestEditMatchesAgainstTheDocumentNotDisk(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package disk\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.go": openDoc("doc-a", "package draft\n", true)}}
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := edit.Run(context.Background(), map[string]any{
		"path": "a.go", "old_string": "disk", "new_string": "agent",
	}, editorCtx(dir, docs))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "EDIT_OLD_STRING_NOT_FOUND" {
		t.Fatalf("err = %v, want EDIT_OLD_STRING_NOT_FOUND against the editor text", err)
	}
}

// Retries preserve the agent read even when the host reports a newer head.
func TestEditRetriesKeepTheAgentRead(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644))
	doc := openDoc("doc-a", "one\ntarget\n", true)
	docs := &fakeEditorDocuments{
		docs: map[string]*tools.EditorDocumentText{"r1/a.txt": doc},
		moveOnce: func(d *tools.EditorDocumentText) {
			d.Text = "one\ntyped\ntarget\n"
			d.Revision++
		},
	}
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := edit.Run(context.Background(), map[string]any{
		"path": "a.txt", "old_string": "target", "new_string": "done",
	}, editorCtx(dir, docs))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "EDITOR_DOCUMENT_CHANGING" {
		t.Fatalf("stale edit rejection: %v", err)
	}
	if len(docs.applied) != editorDocumentRetries {
		t.Fatalf("attempts: %d", len(docs.applied))
	}
	for _, applied := range docs.applied {
		if applied.ExpectedRevision != 4 || applied.Content != "one\ndone\n" {
			t.Fatalf("retry adopted unseen text: %+v", applied)
		}
	}
}

// A document that never stops moving is reported, not written.
func TestEditReportsADocumentThatKeepsChanging(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644))
	docs := &fakeEditorDocuments{
		docs:        map[string]*tools.EditorDocumentText{"r1/a.txt": openDoc("doc-a", "target\n", true)},
		alwaysMoved: true,
	}
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := edit.Run(context.Background(), map[string]any{
		"path": "a.txt", "old_string": "target", "new_string": "done",
	}, editorCtx(dir, docs))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "EDITOR_DOCUMENT_CHANGING" {
		t.Fatalf("err = %v, want EDITOR_DOCUMENT_CHANGING", err)
	}
	if len(docs.applied) != editorDocumentRetries {
		t.Fatalf("applies = %d, want the retry budget", len(docs.applied))
	}
}

// When disk moved outside the editor the document holds the edit unsaved,
// and the receipt says so instead of claiming a save.
func TestEditStatesAnUnsavedLanding(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644))
	docs := &fakeEditorDocuments{
		docs:    map[string]*tools.EditorDocumentText{"r1/a.txt": openDoc("doc-a", "target\n", true)},
		unsaved: true,
	}
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}
	out, err := edit.Run(context.Background(), map[string]any{
		"path": "a.txt", "old_string": "target", "new_string": "done",
	}, editorCtx(dir, docs))
	testutil.FailErr(t, "edit", err)
	if !strings.Contains(out, "not saved to disk") || !strings.Contains(out, "waits for the person's merge") || !strings.Contains(out, "still holds its earlier content") {
		t.Fatalf("receipt = %q, want the unsaved landing and the stale disk stated", out)
	}
}

// write, replace_lines, and code_rewrite share the landing.
func TestWriteAndReplaceLinesLandInTheOpenDocument(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("disk\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.txt": openDoc("doc-a", "one\ntwo\n", true)}}
	tctx := editorCtx(dir, docs)

	write := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := write.Run(context.Background(), map[string]any{"path": "a.txt", "content": "whole\n"}, tctx)
	testutil.FailErr(t, "write", err)
	if got := docs.docs["r1/a.txt"].Text; got != "whole\n" {
		t.Fatalf("write landed %q in the document", got)
	}

	docs.docs["r1/a.txt"].Text = "one\ntwo\n"
	docs.docs["r1/a.txt"].Revision++
	_, err = (&surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(), map[string]any{"path": "a.txt"}, tctx)
	testutil.FailErr(t, "read the changed document", err)
	replace := &ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}
	_, err = replace.Run(context.Background(), map[string]any{
		"path": "a.txt", "start_line": 2, "end_line": 2, "new_content": "TWO",
	}, tctx)
	testutil.FailErr(t, "replace_lines", err)
	if got := docs.docs["r1/a.txt"].Text; got != "one\nTWO\n" {
		t.Fatalf("replace_lines landed %q in the document", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	testutil.FailErr(t, "read disk", err)
	if string(raw) != "disk\n" {
		t.Fatalf("document writes reached disk directly: %q", raw)
	}
}

// A write to a path nobody has open is a file write.
func TestWriteToAClosedPathIsAFileWrite(t *testing.T) {
	dir := t.TempDir()
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{}}
	write := &WriteTool{Boundary: nativefixture.Boundary(t)}
	out, err := write.Run(context.Background(), map[string]any{"path": "new.txt", "content": "fresh\n"}, editorCtx(dir, docs))
	testutil.FailErr(t, "write", err)
	raw, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	testutil.FailErr(t, "read disk", err)
	if string(raw) != "fresh\n" || len(docs.applied) != 0 || strings.Contains(out, "editor document") {
		t.Fatalf("closed-path write: disk=%q applies=%d out=%q", raw, len(docs.applied), out)
	}
}

// A worker on its private branch never sees the person's documents: its
// tree is a copy, and the documents belong to the project tree.
func TestWorkerBranchReadsTheFile(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("disk\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.txt": openDoc("doc-a", "draft\n", true)}}
	tctx := editorCtx(dir, docs)
	tctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := read.Run(context.Background(), map[string]any{"path": "a.txt"}, tctx)
	testutil.FailErr(t, "read", err)
	if resp := readContent(t, out); resp.Content != "     1: disk" {
		t.Fatalf("worker read = %q, want the file", resp.Content)
	}
}

// A search reads the person's unsaved text for files they have open, and
// says how many it served that way.
func TestGrepSearchesOpenDrafts(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write a", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("disk needle\n"), 0o644))
	testutil.FailErr(t, "write b", os.WriteFile(filepath.Join(dir, "b.txt"), []byte("nothing here\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{
		"r1/a.txt": openDoc("doc-a", "draft only\n", true),
		"r1/b.txt": openDoc("doc-b", "draft needle\n", true),
	}}
	grep := &surveytools.GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := grep.Run(context.Background(), map[string]any{"pattern": "needle"}, editorCtx(dir, docs))
	testutil.FailErr(t, "grep", err)
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 || matches[0]["path"] != "b.txt" {
		t.Fatalf("matches = %v, want only the draft hit in b.txt", matches)
	}
	var resp struct {
		FilesFromEditor int `json:"files_from_editor"`
	}
	testutil.FailErr(t, "decode grep", json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &resp))
	if resp.FilesFromEditor != 2 {
		t.Fatalf("files_from_editor = %d, want 2", resp.FilesFromEditor)
	}
}

// A document lookup that fails falls back to the file and says nothing
// about the editor; the fallback is still a true reading of the file.
func TestDraftLookupFailureSearchesTheFile(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("disk needle\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{}, dirtyErr: errors.New("store closed")}
	grep := &surveytools.GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := grep.Run(context.Background(), map[string]any{"pattern": "needle"}, editorCtx(dir, docs))
	testutil.FailErr(t, "grep", err)
	if matches := nativefixture.GrepMatches(t, out); len(matches) != 1 {
		t.Fatalf("matches = %v, want the file hit", matches)
	}
}

func TestDocumentLookupFailureCannotBecomeAFileWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	testutil.FailErr(t, "write file", os.WriteFile(path, []byte("disk"), 0o644))
	lookupErr := errors.New("document store unavailable")
	docs := &fakeEditorDocuments{openErr: lookupErr}
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := edit.Run(t.Context(), map[string]any{"path": "a.txt", "old_string": "disk", "new_string": "replacement"}, editorCtx(dir, docs))
	if !errors.Is(err, lookupErr) {
		t.Fatalf("edit error = %v", err)
	}
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read unchanged file", err)
	if string(raw) != "disk" {
		t.Fatalf("file overwritten: %q", raw)
	}
}

func (f *fakeEditorDocuments) ApplyAgentEdits(ctx context.Context, projectID string, edits []tools.EditorDocumentEdit) ([]tools.EditorDocumentApplied, error) {
	f.batches = append(f.batches, append([]tools.EditorDocumentEdit(nil), edits...))
	for _, edit := range edits {
		found := false
		for _, doc := range f.docs {
			if doc.ID == edit.DocumentID {
				found = true
				if doc.Revision != edit.ExpectedRevision {
					return nil, tools.ErrEditorDocumentMoved
				}
			}
		}
		if !found {
			return nil, errors.New("unknown document")
		}
	}
	results := make([]tools.EditorDocumentApplied, len(edits))
	for i, edit := range edits {
		result, err := f.ApplyAgentEdit(ctx, projectID, edit)
		if err != nil {
			return nil, err
		}
		results[i] = result
	}
	return results, nil
}

func (f *fakeEditorDocuments) PreviewAgentEdits(_ context.Context, _ string, edits []tools.EditorDocumentEdit) ([]tools.EditorDocumentPreview, error) {
	out := make([]tools.EditorDocumentPreview, len(edits))
	for i, edit := range edits {
		found := false
		for _, doc := range f.docs {
			if doc.ID != edit.DocumentID {
				continue
			}
			found = true
			out[i] = tools.EditorDocumentPreview{Before: *doc, After: edit.Content, AfterSHA256: textfile.SHA256([]byte(edit.Content)), BeforeBytes: int64(len(doc.Text)), AfterBytes: int64(len(edit.Content))}
		}
		if !found {
			return nil, errors.New("unknown document")
		}
	}
	return out, nil
}

func TestMutationRequiresASuccessfulRead(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "seed disk", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base\n"), 0o644))
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{"r1/a.txt": openDoc("doc-a", "base\n", false)}}
	tctx := editorCtx(dir, docs)
	clear(docs.reads)
	write := &WriteTool{Boundary: nativefixture.Boundary(t)}
	args := map[string]any{"path": "a.txt", "content": "changed\n"}
	assertReadRequired := func() {
		t.Helper()
		_, err := write.Run(t.Context(), args, tctx)
		reject := toolrejection.AsToolReject(err)
		if reject == nil || reject.Code != "EDITOR_DOCUMENT_READ_REQUIRED" {
			t.Fatalf("read requirement: %v", err)
		}
	}
	assertReadRequired()
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	if _, err := read.Run(t.Context(), map[string]any{"path": "a.txt", "offset": 100}, tctx); err == nil {
		t.Fatal("invalid read succeeded")
	}
	assertReadRequired()
	_, err := read.Run(t.Context(), map[string]any{"path": "a.txt"}, tctx)
	testutil.FailErr(t, "read document", err)
	_, err = write.Run(t.Context(), args, tctx)
	testutil.FailErr(t, "write from delivered read", err)
}

func (f *fakeEditorDocuments) FreezeAgentReads(_ string, sessionID string) map[string]int64 {
	out := make(map[string]int64)
	for key, doc := range f.reads {
		if strings.HasPrefix(key, sessionID+"/") {
			out[doc.ID] = doc.Revision
		}
	}
	return out
}

func TestRewritePreviewDoesNotRequireOrAuthorizeAnAgentRead(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{
		"r1/a.go": {ID: "doc", Revision: 1, Text: "package main\nfunc f() { println(1) }\n"},
	}}
	tctx := editorCtx(dir, fake)
	fake.reads = nil
	resolved := projectpaths.Resolved{Root: tctx.Source.Roots[0], ScopeRel: "a.go", DisplayPath: "a.go"}
	_, err := loadRewriteSource(t.Context(), tctx, resolved, true)
	testutil.FailErr(t, "preview without a prior read", err)
	_, err = loadRewriteSource(t.Context(), tctx, resolved, false)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "EDITOR_DOCUMENT_READ_REQUIRED" {
		t.Fatalf("preview authorized an unseen full snapshot: %v", err)
	}
}
