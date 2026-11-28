package command

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubWindowSourceObserver struct {
	commits int
}

func (s *stubWindowSourceObserver) CommitDeliveredSource(context.Context, string, evidence.DeliveredSourceObservation) (string, error) {
	s.commits++
	return "read#1", nil
}

type stubWindowEditorDocs struct {
	tools.EditorDocuments
	doc        tools.EditorDocumentText
	remembered []tools.EditorDocumentText
}

func (s *stubWindowEditorDocs) OpenDocument(context.Context, string, sourcebranch.ID, string, string) (tools.EditorDocumentText, bool, error) {
	return s.doc, true, nil
}

func (s *stubWindowEditorDocs) RememberAgentRead(_ string, _ string, doc tools.EditorDocumentText) {
	s.remembered = append(s.remembered, doc)
}

func TestCommitProgramWindowsReadBasis(t *testing.T) {
	rootDir := t.TempDir()
	docText := "alpha\nbeta\ngamma\n"
	lines := toolkit.SplitLines(docText)
	fullBody := hostmarker.FormatNumberedLines(lines, 1)
	partialBody := hostmarker.FormatNumberedLines(lines[1:], 2)
	staleBody := hostmarker.FormatNumberedLines(toolkit.SplitLines("alpha\nold\ngamma\n"), 1)

	makeCase := func(w tools.ProgramWindow, delivered string) (*stubWindowEditorDocs, *stubWindowSourceObserver) {
		docs := &stubWindowEditorDocs{
			doc: tools.EditorDocumentText{
				ID:       "doc-1",
				Revision: 3,
				Text:     docText,
			},
		}
		obs := &stubWindowSourceObserver{}
		rt := &ProgramRuntime{SourceObserver: obs}
		tc := tools.ToolContext{
			Identity: tools.InvocationIdentity{
				ProjectID: "proj-1",
				SessionID: "sess-1",
			},
			Source: tools.InvocationSource{
				ActiveRootID:        "root-1",
				SourceWorkspaceKind: api.SourceWorkspaceKindProject,
				Roots: []projectroot.RootRef{
					{ID: "root-1", Path: rootDir},
				},
				EditorDocuments: docs,
			},
			Effects: tools.InvocationEffects{
				Out: &tools.ToolInvocationOut{},
			},
		}
		windows := &tools.ProgramWindows{}
		ctx := tools.WithProgramWindows(t.Context(), windows)
		tools.RecordProgramWindow(ctx, w)
		run := &programRun{
			tc:            tc,
			parentReceipt: "resp-1",
			windows:       windows,
		}
		rt.commitProgramWindows(ctx, run, "prog-1", "output:program:prog-1:stdout", []deliveredOutputPart{{data: []byte(delivered)}})
		return docs, obs
	}

	encodedFull, err := surveyjson.Marshal(fullBody)
	if err != nil {
		t.Fatalf("marshal full body: %v", err)
	}
	encodedPartial, err := surveyjson.Marshal(partialBody)
	if err != nil {
		t.Fatalf("marshal partial body: %v", err)
	}
	encodedStale, err := surveyjson.Marshal(staleBody)
	if err != nil {
		t.Fatalf("marshal stale body: %v", err)
	}

	// 1. Positive: exact whole shared-document window delivered to model -> credits read basis.
	docs, obs := makeCase(tools.ProgramWindow{
		Path:             "target.txt",
		RootID:           "root-1",
		ScopeRel:         "target.txt",
		DocumentID:       "doc-1",
		DocumentRevision: 3,
		StartLine:        1,
		EndLine:          len(lines),
		Body:             fullBody,
	}, `{"body":`+string(encodedFull)+`}`)
	if obs.commits != 1 {
		t.Fatalf("expected 1 delivered source commit, got %d", obs.commits)
	}
	if len(docs.remembered) != 1 || docs.remembered[0].ID != "doc-1" || docs.remembered[0].Revision != 3 {
		t.Fatalf("expected RememberAgentRead for doc-1 rev 3, got %+v", docs.remembered)
	}

	// 2. Negative: partial window (StartLine=2) -> must NOT credit read basis.
	docs, _ = makeCase(tools.ProgramWindow{
		Path:             "target.txt",
		RootID:           "root-1",
		ScopeRel:         "target.txt",
		DocumentID:       "doc-1",
		DocumentRevision: 3,
		StartLine:        2,
		EndLine:          len(lines),
		Body:             partialBody,
	}, `{"body":`+string(encodedPartial)+`}`)
	if len(docs.remembered) != 0 {
		t.Fatalf("partial window must not credit read basis, got %+v", docs.remembered)
	}

	// 3. Negative: clipped/screened output (encoded body absent from delivered parts) -> must NOT credit.
	docs, obs = makeCase(tools.ProgramWindow{
		Path:             "target.txt",
		RootID:           "root-1",
		ScopeRel:         "target.txt",
		DocumentID:       "doc-1",
		DocumentRevision: 3,
		StartLine:        1,
		EndLine:          len(lines),
		Body:             fullBody,
	}, `{"body":"clipped..."}`)
	if obs.commits != 0 || len(docs.remembered) != 0 {
		t.Fatalf("clipped output must not commit or credit read basis, commits=%d remembered=%+v", obs.commits, docs.remembered)
	}

	// 4. Negative: inline / unauthenticated window (empty RootID) -> must NOT credit.
	docs, _ = makeCase(tools.ProgramWindow{
		Path:             filepath.Base("target.txt"),
		RootID:           "",
		ScopeRel:         "target.txt",
		DocumentID:       "doc-1",
		DocumentRevision: 3,
		StartLine:        1,
		EndLine:          len(lines),
		Body:             fullBody,
	}, `{"body":`+string(encodedFull)+`}`)
	if len(docs.remembered) != 0 {
		t.Fatalf("unauthenticated window without RootID must not credit read basis, got %+v", docs.remembered)
	}

	// 5. Negative: stale content (delivered body differs from current open document text) -> must NOT credit.
	docs, _ = makeCase(tools.ProgramWindow{
		Path:             "target.txt",
		RootID:           "root-1",
		ScopeRel:         "target.txt",
		DocumentID:       "doc-1",
		DocumentRevision: 3,
		StartLine:        1,
		EndLine:          len(lines),
		Body:             staleBody,
	}, `{"body":`+string(encodedStale)+`}`)
	if len(docs.remembered) != 0 {
		t.Fatalf("stale content window must not credit read basis, got %+v", docs.remembered)
	}

	// 6. Negative: document replaced/reopened (different DocumentID with identical bytes) -> must NOT credit.
	docs, _ = makeCase(tools.ProgramWindow{
		Path:             "target.txt",
		RootID:           "root-1",
		ScopeRel:         "target.txt",
		DocumentID:       "doc-old",
		DocumentRevision: 3,
		StartLine:        1,
		EndLine:          len(lines),
		Body:             fullBody,
	}, `{"body":`+string(encodedFull)+`}`)
	if len(docs.remembered) != 0 {
		t.Fatalf("replaced/reopened document with different ID must not credit read basis, got %+v", docs.remembered)
	}

	// 7. Negative: revision bumped with otherwise equal bytes -> must NOT credit.
	docs, _ = makeCase(tools.ProgramWindow{
		Path:             "target.txt",
		RootID:           "root-1",
		ScopeRel:         "target.txt",
		DocumentID:       "doc-1",
		DocumentRevision: 2,
		StartLine:        1,
		EndLine:          len(lines),
		Body:             fullBody,
	}, `{"body":`+string(encodedFull)+`}`)
	if len(docs.remembered) != 0 {
		t.Fatalf("stale revision with identical bytes must not credit read basis, got %+v", docs.remembered)
	}
}
