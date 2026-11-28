package toolhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
)

type stubEditorDocs struct {
	mu              sync.Mutex
	docs            map[string]*tools.EditorDocumentText
	diskPaths       map[string]string
	reads           map[string]tools.EditorDocumentText
	applyCalls      int
	readWholeCalled int
	readSpansCalled int
}

func newStubEditorDocs() *stubEditorDocs {
	return &stubEditorDocs{
		docs:      map[string]*tools.EditorDocumentText{},
		diskPaths: map[string]string{},
		reads:     map[string]tools.EditorDocumentText{},
	}
}

func (s *stubEditorDocs) addOpen(rootID, relPath, absPath, id, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := textfile.SHA256([]byte(content))
	s.docs[rootID+"/"+relPath] = &tools.EditorDocumentText{
		ID:         id,
		RootID:     rootID,
		Path:       relPath,
		Revision:   1,
		Text:       content,
		BaseSHA256: sum,
		Encoding:   textfile.UTF8,
		EOL:        "lf",
	}
	s.diskPaths[id] = absPath
}

func (s *stubEditorDocs) docByPath(rootID, relPath string) tools.EditorDocumentText {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.docs[rootID+"/"+relPath]
}

func (s *stubEditorDocs) FreezeAgentReads(string, string) map[string]documentcore.ReadBasis {
	return nil
}

func (s *stubEditorDocs) RememberAgentRead(_ string, sessionID string, doc tools.EditorDocumentText) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc.ReadWhole = true
	doc.ReadSpans = nil
	s.reads[sessionID+"/"+doc.ID] = doc
	s.readWholeCalled++
}

func (s *stubEditorDocs) RememberAgentReadSpans(_ string, sessionID string, doc tools.EditorDocumentText, spans []documentcore.LineSpan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc.ReadWhole = false
	doc.ReadSpans = append([]documentcore.LineSpan(nil), spans...)
	s.reads[sessionID+"/"+doc.ID] = doc
	s.readSpansCalled++
}

func (s *stubEditorDocs) AdvanceAgentRead(projectID, sessionID string, frozen *tools.AgentReadBases, edit tools.EditorDocumentEdit, doc tools.EditorDocumentText) {
	if doc.Text == strings.ReplaceAll(edit.Content, "\r\n", "\n") {
		s.RememberAgentRead(projectID, sessionID, doc)
		frozen.AdvanceBasis(doc.ID, documentcore.ReadBasis{Revision: doc.Revision, Whole: doc.ReadWhole, Spans: doc.ReadSpans})
	}
}

func (s *stubEditorDocs) AgentReadBase(_ context.Context, _, sessionID, documentID string, _ *tools.AgentReadBases) (tools.EditorDocumentText, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.reads[sessionID+"/"+documentID]
	if !ok {
		return tools.EditorDocumentText{}, tools.ErrEditorReadRequired
	}
	return doc, nil
}

func (s *stubEditorDocs) OpenDocument(_ context.Context, _ string, _ sourcebranch.ID, rootID, path string) (tools.EditorDocumentText, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[rootID+"/"+path]
	if !ok {
		return tools.EditorDocumentText{}, false, nil
	}
	return *doc, true, nil
}

func (s *stubEditorDocs) PreviewAgentEdits(_ context.Context, _ string, edits []tools.EditorDocumentEdit) ([]tools.EditorDocumentPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]tools.EditorDocumentPreview, 0, len(edits))
	for _, edit := range edits {
		for _, doc := range s.docs {
			if doc.ID == edit.DocumentID {
				out = append(out, tools.EditorDocumentPreview{
					Before:      *doc,
					After:       edit.Content,
					AfterSHA256: textfile.SHA256([]byte(edit.Content)),
					BeforeBytes: int64(len(doc.Text)),
					AfterBytes:  int64(len(edit.Content)),
				})
			}
		}
	}
	return out, nil
}

func (s *stubEditorDocs) ApplyAgentEdit(_ context.Context, _ string, edit tools.EditorDocumentEdit) (tools.EditorDocumentApplied, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyCalls++
	for _, doc := range s.docs {
		if doc.ID == edit.DocumentID {
			doc.Revision++
			doc.Text = edit.Content
			doc.BaseSHA256 = textfile.SHA256([]byte(edit.Content))
			if diskPath := s.diskPaths[doc.ID]; diskPath != "" {
				if err := os.WriteFile(diskPath, []byte(edit.Content), 0o644); err != nil {
					return tools.EditorDocumentApplied{}, err
				}
			}
			return tools.EditorDocumentApplied{Document: *doc, Saved: true}, nil
		}
	}
	return tools.EditorDocumentApplied{}, tools.ErrEditorDocumentMoved
}

func (s *stubEditorDocs) ApplyAgentEdits(ctx context.Context, projectID string, edits []tools.EditorDocumentEdit) ([]tools.EditorDocumentApplied, error) {
	out := make([]tools.EditorDocumentApplied, 0, len(edits))
	for _, edit := range edits {
		applied, err := s.ApplyAgentEdit(ctx, projectID, edit)
		if err != nil {
			return nil, err
		}
		out = append(out, applied)
	}
	return out, nil
}

func (s *stubEditorDocs) VisitUnsavedDocuments(context.Context, string, sourcebranch.ID, string, string, bool, func(tools.EditorDocumentState) error) error {
	return nil
}
func (s *stubEditorDocs) RetainRevision(context.Context, string, string, int64) error { return nil }
func (s *stubEditorDocs) SaveForAgentOperation(context.Context, string, sourcebranch.ID, string, string, bool, tools.EditorAgentCall) error {
	return nil
}
func (s *stubEditorDocs) SaveForAgentLaunch(context.Context, string, sourcebranch.ID, tools.EditorAgentCall, func(string, int64) bool) (tools.EditorLaunchSave, error) {
	return tools.EditorLaunchSave{}, nil
}
func (s *stubEditorDocs) DirtyDocuments(context.Context, string, sourcebranch.ID) ([]tools.EditorDocumentPath, error) {
	return nil, nil
}
func (s *stubEditorDocs) VisitAgentPublications(context.Context, string, sourcebranch.ID, string, int, string, func(tools.EditorDocumentPublicationCandidate) error) error {
	return nil
}

func TestCodeRewriteConversationalAdmissionControls(t *testing.T) {
	f := newComposedProgramFixture(t)
	docs := newStubEditorDocs()
	configure := f.Configure
	var rootID string
	f.Configure = func(tc *tools.ToolContext) {
		if configure != nil {
			configure(tc)
		}
		rootID = tc.Source.ActiveRootID
		tc.Source.EditorDocuments = docs
	}
	// Resolve active rootID once via a harmless command.
	_ = callComposedProgram(t, f, `true`)

	initial := "package main\n\nfunc run() string { return wrap(\"alpha\") }\n"
	openPath := filepath.Join(f.Dir, "open.go")
	testutil.FailErr(t, "seed open.go", os.WriteFile(openPath, []byte(initial), 0o644))
	docs.addOpen(rootID, "open.go", openPath, "doc-open", initial)

	assertUnchanged := func(t *testing.T, label string) {
		t.Helper()
		if got := docs.docByPath(rootID, "open.go"); got.Revision != 1 || got.Text != initial {
			t.Fatalf("%s: open.go doc mutated: rev=%d text=%q", label, got.Revision, got.Text)
		}
		disk, err := os.ReadFile(openPath)
		testutil.FailErr(t, "read open.go disk", err)
		if string(disk) != initial {
			t.Fatalf("%s: open.go disk mutated: %q", label, disk)
		}
		if docs.applyCalls != 0 {
			t.Fatalf("%s: ApplyAgentEdit called %d times, want 0", label, docs.applyCalls)
		}
	}

	t.Run("open_doc_absent_read_base_refuses", func(t *testing.T) {
		outcome := runFormTool(t, f, "code_rewrite", toolreach.CodeRewriteArgv, map[string]any{
			"path": "open.go", "pattern": "wrap($A)", "rewrite": "unwrap($A)",
		})
		if outcome.Refusal == nil || outcome.Refusal.Code != "EDITOR_DOCUMENT_READ_REQUIRED" || outcome.CompletedEffects != 0 {
			t.Fatalf("absent read base: outcome=%+v refusal=%+v", outcome, outcome.Refusal)
		}
		assertUnchanged(t, "absent read base")
	})

	t.Run("open_doc_partial_target_line_read_refuses", func(t *testing.T) {
		readOut := callComposedProgram(t, f, `sed -n '3p' open.go`)
		if readOut.Status != 0 || !strings.Contains(readOut.Stdout, `wrap("alpha")`) {
			t.Fatalf("partial read failed: %+v", readOut)
		}
		if docs.readSpansCalled == 0 {
			t.Fatalf("partial read did not record read spans")
		}
		outcome := runFormTool(t, f, "code_rewrite", toolreach.CodeRewriteArgv, map[string]any{
			"path": "open.go", "pattern": "wrap($A)", "rewrite": "unwrap($A)",
		})
		if outcome.Refusal == nil || outcome.Refusal.Code != "EDITOR_DOCUMENT_READ_REQUIRED" || outcome.CompletedEffects != 0 {
			t.Fatalf("partial read base: outcome=%+v refusal=%+v", outcome, outcome.Refusal)
		}
		assertUnchanged(t, "partial read base")
	})

	t.Run("open_doc_whole_read_base_succeeds", func(t *testing.T) {
		readOut := callComposedProgram(t, f, `cat open.go`)
		if readOut.Status != 0 || readOut.Stdout != initial {
			t.Fatalf("whole read failed: %+v", readOut)
		}
		if docs.readWholeCalled == 0 {
			t.Fatalf("whole read did not record whole-document read base")
		}
		outcome := runFormTool(t, f, "code_rewrite", toolreach.CodeRewriteArgv, map[string]any{
			"path": "open.go", "pattern": "wrap($A)", "rewrite": "unwrap($A)",
		})
		if outcome.Refusal != nil || outcome.State != "completed" || outcome.Status != 0 || outcome.CompletedEffects != 1 {
			t.Fatalf("whole read rewrite: outcome=%+v refusal=%+v", outcome, outcome.Refusal)
		}
		gotDoc := docs.docByPath(rootID, "open.go")
		if gotDoc.Revision != 2 || !strings.Contains(gotDoc.Text, `unwrap("alpha")`) {
			t.Fatalf("whole read doc = rev %d text=%q", gotDoc.Revision, gotDoc.Text)
		}
		disk, err := os.ReadFile(openPath)
		testutil.FailErr(t, "read open.go after rewrite", err)
		if string(disk) != gotDoc.Text {
			t.Fatalf("disk=%q, want %q", disk, gotDoc.Text)
		}
		if docs.applyCalls != 1 {
			t.Fatalf("ApplyAgentEdit calls = %d, want 1", docs.applyCalls)
		}
	})

	t.Run("closed_doc_control_succeeds", func(t *testing.T) {
		closedPath := filepath.Join(f.Dir, "closed.go")
		testutil.FailErr(t, "seed closed.go", os.WriteFile(closedPath, []byte(initial), 0o644))
		outcome := runFormTool(t, f, "code_rewrite", toolreach.CodeRewriteArgv, map[string]any{
			"path": "closed.go", "pattern": "wrap($A)", "rewrite": "unwrap($A)",
		})
		if outcome.Refusal != nil || outcome.State != "completed" || outcome.Status != 0 {
			t.Fatalf("closed doc rewrite: outcome=%+v refusal=%+v", outcome, outcome.Refusal)
		}
		disk, err := os.ReadFile(closedPath)
		testutil.FailErr(t, "read closed.go", err)
		if !strings.Contains(string(disk), `unwrap("alpha")`) {
			t.Fatalf("closed.go = %q", disk)
		}
	})

	t.Run("native_command_code_rewrite_stays_execution_pinned", func(t *testing.T) {
		pinnedPath := filepath.Join(f.Dir, "pinned.go")
		testutil.FailErr(t, "seed pinned.go", os.WriteFile(pinnedPath, []byte(initial), 0o644))
		docs.addOpen(rootID, "pinned.go", pinnedPath, "doc-pinned", initial)
		outcome := callComposedProgram(t, f, `code-rewrite --path pinned.go --pattern 'wrap($A)' --rewrite 'unwrap($A)'`)
		if outcome.Refusal != nil || outcome.State != "completed" || outcome.Status != 0 {
			t.Fatalf("command code-rewrite: outcome=%+v refusal=%+v", outcome, outcome.Refusal)
		}
		gotDoc := docs.docByPath(rootID, "pinned.go")
		if gotDoc.Revision != 2 || !strings.Contains(gotDoc.Text, `unwrap("alpha")`) {
			t.Fatalf("pinned.go doc = rev %d text=%q", gotDoc.Revision, gotDoc.Text)
		}
	})
}
