package editordoc

import (
	"errors"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestPreparedAgentPreviewShowsRebasedTextAndFencesReview(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "first\nsecond\nthird\n"})
	base := f.open(t, "a.txt")
	base, err := f.service.Pin(t.Context(), base.ProjectID, base.ID, base.Revision)
	testutil.FailErr(t, "pin agent read", err)
	input := agentEdit(base, "FIRST\nsecond\nthird\n")
	current, err := f.service.ReplaceSnapshot(t.Context(), base.ID, base.ProjectID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: base.Revision},
		Content:         "first\nSECOND\nthird\n", EOL: "lf"})
	testutil.FailErr(t, "type on another line before review", err)
	previews, err := f.service.PreviewAgentEdits(t.Context(), []AgentEdit{input})
	testutil.FailErr(t, "prepare current preview", err)
	preview := previews[0]
	if preview.Before.Revision != current.Revision || preview.Before.Draft != "first\nSECOND\nthird\n" || preview.After != "FIRST\nSECOND\nthird\n" {
		t.Fatalf("preview lost concurrent text: %+v", preview)
	}
	input.ReviewedRevision = preview.Before.Revision
	later, err := f.service.ReplaceSnapshot(t.Context(), current.ID, current.ProjectID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: current.Revision},
		Content:         "first\nSECOND\nTHIRD\n", EOL: "lf"})
	testutil.FailErr(t, "type while preview is open", err)
	_, err = f.service.ApplyAgentEdit(t.Context(), input)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("changed review accepted: %v", err)
	}
	stored, err := f.store.Get(t.Context(), later.ID)
	testutil.FailErr(t, "read rejected review head", err)
	if stored.Draft != later.Draft || f.disk(t, "a.txt") != base.Draft {
		t.Fatal("stale review changed a document or file")
	}
	previews, err = f.service.PreviewAgentEdits(t.Context(), []AgentEdit{input})
	testutil.FailErr(t, "prepare new review", err)
	input.ReviewedRevision = previews[0].Before.Revision
	landed, err := f.service.ApplyAgentEdit(t.Context(), input)
	testutil.FailErr(t, "accept reviewed edit", err)
	if !landed.Saved || landed.Document.Draft != previews[0].After {
		t.Fatal("publication differs from its reviewed preview")
	}
}

func TestAgentLineGuardsIncludeBoundaryTyping(t *testing.T) {
	for _, pair := range [][2]string{
		{"fooBar\n", "prefix fooBar\n"},
		{"fooBar\n", "fooBar suffix\n"},
		{"fooBar", "fooBar suffix"},
	} {
		t.Run(pair[1], func(t *testing.T) {
			f := newAgentFixture(t, map[string]string{"a.txt": pair[0]})
			doc := f.open(t, "a.txt")
			pinned, err := f.service.Pin(t.Context(), doc.ProjectID, doc.ID, doc.Revision)
			testutil.FailErr(t, "pin source line", err)
			_, err = f.service.ReplaceSnapshot(t.Context(), doc.ID, doc.ProjectID, SnapshotReplacement{
				DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
				Content:         pair[1], EOL: "lf"})
			testutil.FailErr(t, "type at line boundary", err)
			_, err = f.service.ApplyAgentEdit(t.Context(), agentEdit(pinned, "fetchBar\n"))
			if !errors.Is(err, ErrRevisionConflict) {
				t.Fatalf("boundary typing was not guarded: %v", err)
			}
		})
	}
}
