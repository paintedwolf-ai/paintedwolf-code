package editoradmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// tokenRange locates the relay token by rune offsets, as an editor selects it.
func tokenRange(revision int64) wire.SecretMarkRange {
	runes, target := []rune(fileWithSecret), []rune(relayToken)
	for i := 0; i+len(target) <= len(runes); i++ {
		if string(runes[i:i+len(target)]) == relayToken {
			return wire.SecretMarkRange{Revision: revision, Start: i, End: i + len(target)}
		}
	}
	return wire.SecretMarkRange{Revision: revision, Start: -1, End: -1}
}

func (f *editorFixture) preview(t *testing.T, d wire.EditorDocument, rng wire.SecretMarkRange) wire.SecretMarkPreview {
	t.Helper()
	response := f.serve(t, http.MethodPost, f.documentPath(d.ID, "/secret-spans/preview"), rng)
	return decodeStatus[wire.SecretMarkPreview](t, "preview mark", response, http.StatusOK)
}

func TestSecretMarkPreviewPredictsRefusals(t *testing.T) {
	f := newEditorFixture(t)
	d := f.open(t, "staging.ts", "window")
	token := tokenRange(d.Revision)
	eligible := f.preview(t, d, token)
	if !eligible.Eligible || eligible.Start != token.Start || eligible.End != token.End || eligible.RuneLength != len(relayToken) {
		t.Fatalf("token preview = %+v", eligible)
	}
	short := f.preview(t, d, wire.SecretMarkRange{Revision: d.Revision, Start: token.Start, End: token.Start + 2})
	if short.Eligible || short.Reason != string(secretspan.IneligibleTooShort) {
		t.Fatalf("short preview = %+v", short)
	}
	outside := f.preview(t, d, wire.SecretMarkRange{Revision: d.Revision, Start: 0, End: 1 << 20})
	if outside.Eligible || outside.Reason != string(secretspan.IneligibleOutOfRange) {
		t.Fatalf("out-of-range preview = %+v", outside)
	}
	stale := f.serve(t, http.MethodPost, f.documentPath(d.ID, "/secret-spans/preview"), wire.SecretMarkRange{Revision: d.Revision + 9, Start: token.Start, End: token.End})
	expectCode(t, "stale revision", stale, wire.ApiErrorCodeEditorRevisionConflict)
	unselected := f.serve(t, http.MethodPost, f.documentPath(d.ID, "/secret-spans/preview"), wire.SecretMarkRange{Start: token.Start, End: token.End})
	expectCode(t, "missing revision", unselected, wire.ApiErrorCodeEditorRevisionConflict)
	missing := f.serve(t, http.MethodPost, f.documentPath(uuid.NewString(), "/secret-spans/preview"), token)
	expectCode(t, "missing document", missing, wire.ApiErrorCodeEditorDocumentNotFound)
}

func TestMarkingASecretProtectsTheSelectedBytes(t *testing.T) {
	f := newEditorFixture(t)
	d := f.open(t, "staging.ts", "window")
	token := tokenRange(d.Revision)
	mark := func(body wire.SecretMarkRequest) *httptest.ResponseRecorder {
		return f.serve(t, http.MethodPost, f.documentPath(d.ID, "/secret-spans/mark"), body)
	}
	short := wire.SecretMarkRange{Revision: d.Revision, Start: token.Start, End: token.Start + 2}
	expectCode(t, "short mark", mark(wire.SecretMarkRequest{Range: short, Name: "relay", Purpose: "staging relay"}), wire.ApiErrorCodeInvalidRequest)
	expectCode(t, "unpurposed mark", mark(wire.SecretMarkRequest{Range: token, Name: "relay"}), wire.ApiErrorCodeInvalidRequest)
	stale := wire.SecretMarkRange{Start: token.Start, End: token.End}
	expectCode(t, "stale mark", mark(wire.SecretMarkRequest{Range: stale, Name: "relay", Purpose: "staging relay"}), wire.ApiErrorCodeEditorRevisionConflict)

	response := mark(wire.SecretMarkRequest{Range: token, Name: " relay ", Purpose: "staging relay"})
	if strings.Contains(response.Body.String(), relayToken) {
		t.Fatal("marked metadata exposed the value")
	}
	marked := decodeStatus[wire.ManagedSecret](t, "mark", response, http.StatusOK)
	if marked.Reference == "" || marked.Name != "relay" {
		t.Fatalf("marked secret = %+v", marked)
	}
	protected := f.preview(t, d, token)
	if protected.Eligible || protected.Reason != string(secretspan.IneligibleAlreadyProtected) {
		t.Fatalf("protected preview = %+v", protected)
	}
	again := mark(wire.SecretMarkRequest{Range: token, Name: "relay", Purpose: "staging relay", OperationID: uuid.NewString()})
	expectCode(t, "duplicate mark", again, wire.ApiErrorCodeInvalidRequest)
}

func TestScreenProjectionCompletesInTheBackground(t *testing.T) {
	f := newEditorFixture(t)
	opened := f.open(t, "staging.ts", "window")
	if opened.SecretScreenStatus == wire.SecretScreenUnavailable {
		t.Fatalf("screen status = %q, want screening admitted", opened.SecretScreenStatus)
	}
	d, err := f.h.EditorDocuments.CurrentSnapshot(t.Context(), f.project.ID, opened.ID)
	testutil.FailErr(t, "read document", err)
	var screen *wire.SecretScreen
	testutil.WaitFor(t, 5*time.Second, func() bool {
		status, projected := f.h.EditorScreenProjection(t.Context(), d)
		screen = projected
		return status == wire.SecretScreenComplete
	})
	if screen == nil || screen.ScreenedRevision != d.Revision {
		t.Fatalf("completed screen = %+v", screen)
	}

	spans := f.h.SecretSpans
	f.h.SecretSpans = nil
	status, none := f.h.EditorScreenProjection(t.Context(), d)
	f.h.SecretSpans = spans
	if status != wire.SecretScreenUnavailable || none != nil {
		t.Fatalf("unscreened status = %q", status)
	}
	if status, _ := f.h.EditorScreenProjection(t.Context(), nil); status != wire.SecretScreenUnavailable {
		t.Fatalf("absent document status = %q", status)
	}
}

func TestRefreshRepublishesScreenedDocuments(t *testing.T) {
	f := newEditorFixture(t)
	opened := f.open(t, "staging.ts", "window")
	updates, unsubscribe, err := f.h.Events.Subscribe(t.Context(), events.Subscription{Project: f.project.ID, Viewer: f.owner})
	testutil.FailErr(t, "subscribe to editor events", err)
	defer unsubscribe()

	f.h.RefreshProjectSecretScreens(t.Context(), uuid.NewString())
	f.h.RefreshProjectSecretScreens(t.Context(), f.project.ID)
	deadline := time.After(testutil.Timeout(5 * time.Second))
	for {
		select {
		case envelope := <-updates:
			var event wire.EditorDocumentEvent
			if envelope.Topic != wire.EventTopicEditorDocument || json.Unmarshal(envelope.Data, &event) != nil || event.ID != opened.ID {
				continue
			}
			if event.ContentChanged {
				t.Fatal("a screening refresh reported a content change")
			}
			return
		case <-deadline:
			t.Fatal("refresh published no editor document event")
		}
	}
}

func TestRepublishWithoutEventsOnlyInvalidatesTheScreen(t *testing.T) {
	f := newEditorFixture(t)
	d := &editordoc.Document{ID: uuid.NewString(), ProjectID: f.project.ID, Draft: "token", Revision: 1}
	if !f.h.editorScreens.begin(d.ID, d.ProjectID, "digest") {
		t.Fatal("first screening was not admitted")
	}
	if f.h.editorScreens.begin(d.ID, d.ProjectID, "digest") {
		t.Fatal("a pending screening was admitted twice")
	}
	f.h.finishEditorScreen(t.Context(), *d, "digest")
	if _, ok := f.h.EditorScreen(d.ID, "digest"); !ok {
		t.Fatal("finished screen was not retained for an unpublished document")
	}
	if f.h.editorScreens.complete(d.ID, "digest", &wire.SecretScreen{}) {
		t.Fatal("a completed screen accepted a second completion")
	}
	if got := f.h.editorScreens.documents(""); got[d.ID] != f.project.ID {
		t.Fatalf("screened documents = %v", got)
	}
	hub := f.h.Events
	f.h.Events = nil
	f.h.republishEditorDocument(context.Background(), d)
	f.h.Events = hub
	if _, ok := f.h.EditorScreen(d.ID, "digest"); ok {
		t.Fatal("republish kept a stale screen")
	}
	var absent *Handler
	absent.republishEditorDocument(t.Context(), d)
	f.h.republishEditorDocument(t.Context(), nil)
}

func TestMarkRefusalCopyCoversEveryReason(t *testing.T) {
	seen := map[string]bool{}
	for _, reason := range []secretspan.Ineligible{
		secretspan.IneligibleTooLarge, secretspan.IneligibleTooShort, secretspan.IneligibleEmpty,
		secretspan.IneligibleAlreadyProtected, secretspan.IneligibleOutOfRange,
	} {
		text := markRefusal(reason)
		if text == "" || seen[text] {
			t.Fatalf("refusal copy for %s = %q", reason, text)
		}
		seen[text] = true
	}
	trim := false
	if trimRequested(wire.SecretMarkRange{Trim: &trim}) || !trimRequested(wire.SecretMarkRange{}) {
		t.Fatal("omitted trim must mean trim; explicit false must not")
	}
}
