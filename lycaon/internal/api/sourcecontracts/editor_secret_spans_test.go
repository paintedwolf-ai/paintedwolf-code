package sourcecontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestOpenEditorDocumentDoesNotWaitForSecretScreen(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	if f.FixtureDocument.SecretScreenStatus != wire.SecretScreenPending {
		t.Fatalf("screen status = %q, want pending", f.FixtureDocument.SecretScreenStatus)
	}
	if f.FixtureDocument.SecretScreen != nil {
		t.Fatal("an open response must not wait for or carry the background screen")
	}
	screen := f.AwaitScreen(t)
	if screen.ScreenedRevision != f.FixtureDocument.Revision {
		t.Fatalf("screened revision = %d, document revision = %d",
			screen.ScreenedRevision, f.FixtureDocument.Revision)
	}
	if screen.CatalogVersion == "" {
		t.Fatal("a screen that ran must name its catalog")
	}
}

func TestCompletedSecretScreenPublishesAContentFreeTransition(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	envelope := testutil.Receive(t, "completed screen event", f.Events)
	if envelope.Topic != wire.EventTopicEditorDocument {
		t.Fatalf("topic = %q, want %q", envelope.Topic, wire.EventTopicEditorDocument)
	}
	var event wire.EditorDocumentEvent
	testutil.FailErr(t, "decode editor event", json.Unmarshal(envelope.Data, &event))
	if event.SecretScreenStatus != wire.SecretScreenComplete || event.SecretScreen == nil {
		t.Fatalf("screen transition = status %q screen %v", event.SecretScreenStatus, event.SecretScreen)
	}
	if event.ContentChanged {
		t.Fatal("a highlighting transition must not republish document content")
	}
}

func TestLeaveEditorDocumentReturnsNoSnapshotBody(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	body, err := json.Marshal(wire.LeaveEditorDocumentRequest{ClientID: "test-client"})
	testutil.FailErr(t, "encode release", err)
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, contractfixture.NewAuthedRequest(
		http.MethodPost,
		"/v1/projects/"+f.Project.ID+"/editor-documents/"+f.FixtureDocument.ID+"/leave",
		bytes.NewReader(body),
	))

	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("release response = status %d body %q", w.Code, w.Body.String())
	}
}

func TestCachedSecretScreenProjectsOntoANewerMetadataRevision(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	_ = f.AwaitScreen(t)
	document, err := f.Srv.Sources.Editor.EditorDocuments.CurrentSnapshot(t.Context(), f.Project.ID, f.FixtureDocument.ID)
	testutil.FailErr(t, "load document", err)
	document.Revision++
	status, screen := f.Srv.Sources.Editor.EditorScreenProjection(t.Context(), document)
	if status != wire.SecretScreenComplete || screen == nil {
		t.Fatalf("screen projection = status %q screen %v", status, screen)
	}
	if screen.ScreenedRevision != document.Revision {
		t.Fatalf("screened revision = %d, want %d", screen.ScreenedRevision, document.Revision)
	}
}

func TestScreenCarriesNoSecretBytesOnTheWire(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	raw, err := json.Marshal(f.AwaitScreen(t))
	testutil.FailErr(t, "encode screen", err)
	if bytes.Contains(raw, []byte(contractfixture.RelayToken)) {
		t.Fatalf("the screen carried the value: %s", raw)
	}
}

func TestPreviewTrimsQuotesAndReportsWhatItWouldCapture(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	start, end := f.TokenRange()
	// Select including both surrounding quotes, as a person would.
	w := f.Post(t, "preview", wire.SecretMarkRange{
		Revision: f.FixtureDocument.Revision, Start: start - 1, End: end + 1,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got wire.SecretMarkPreview
	testutil.FailErr(t, "decode preview", json.Unmarshal(w.Body.Bytes(), &got))
	if !got.Eligible {
		t.Fatalf("preview refused with %q", got.Reason)
	}
	if got.Start != start || got.End != end {
		t.Fatalf("range = %d..%d, want %d..%d", got.Start, got.End, start, end)
	}
	if got.TrimmedLeading != 1 || got.TrimmedTrailing != 1 {
		t.Fatalf("trim = %d/%d, want 1/1", got.TrimmedLeading, got.TrimmedTrailing)
	}
	if got.Shape == "" {
		t.Fatal("a preview must carry a shape receipt")
	}
	if bytes.Contains(w.Body.Bytes(), []byte(contractfixture.RelayToken)) {
		t.Fatal("the preview echoed the value back")
	}
}

func TestMarkStoresTheCapabilityAndLeavesTheFileAlone(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	start, end := f.TokenRange()
	before, err := os.ReadFile(filepath.Join(f.Project.Roots[0].Path, "staging.ts"))
	testutil.FailErr(t, "read before", err)

	w := f.Post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.FixtureDocument.Revision, Start: start, End: end},
		Name:  "Staging relay token", Purpose: "Authenticates the staging relay",
		OperationID: "op-1",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var secret wire.ManagedSecret
	testutil.FailErr(t, "decode secret", json.Unmarshal(w.Body.Bytes(), &secret))
	if secret.Origin != "file_marked" {
		t.Fatalf("origin = %q, want file_marked", secret.Origin)
	}
	if secret.Scope != "project" {
		t.Fatalf("scope = %q, want project — a value in a file outlives one chat", secret.Scope)
	}
	if secret.ChatSessionID != nil {
		t.Fatal("a marked value names no owning chat")
	}
	if bytes.Contains(w.Body.Bytes(), []byte(contractfixture.RelayToken)) {
		t.Fatal("the mint response carried the value")
	}

	after, err := os.ReadFile(filepath.Join(f.Project.Roots[0].Path, "staging.ts"))
	testutil.FailErr(t, "read after", err)
	if !bytes.Equal(before, after) {
		t.Fatal("marking must not modify the file")
	}
}

func TestMarkingTheSameRangeTwiceReturnsOneCapability(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	start, end := f.TokenRange()
	request := wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.FixtureDocument.Revision, Start: start, End: end},
		Name:  "Staging relay token", Purpose: "Authenticates the staging relay", OperationID: "op-" + uuid.NewString(),
	}
	first := f.Post(t, "mark", request)
	second := f.Post(t, "mark", request)
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d (%s) and %d (%s)", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	var a, b wire.ManagedSecret
	testutil.FailErr(t, "decode first", json.Unmarshal(first.Body.Bytes(), &a))
	testutil.FailErr(t, "decode second", json.Unmarshal(second.Body.Bytes(), &b))
	if a.Reference != b.Reference {
		t.Fatalf("references diverged: %q vs %q", a.Reference, b.Reference)
	}
}

// Bytes a managed secret already protects mint no second capability; the
// preview predicts the refusal the mark returns.

func TestMarkRefusesBytesAManagedSecretAlreadyProtects(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	start, end := f.TokenRange()
	first := f.Post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.FixtureDocument.Revision, Start: start, End: end},
		Name:  "Staging relay token", Purpose: "Authenticates the staging relay", OperationID: "op-1",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first mark status = %d body=%s", first.Code, first.Body.String())
	}
	// A wider selection still holds the protected bytes.
	rng := wire.SecretMarkRange{Revision: f.FixtureDocument.Revision, Start: start - 1, End: end + 1, Trim: new(bool)}
	preview := f.Post(t, "preview", rng)
	var got wire.SecretMarkPreview
	testutil.FailErr(t, "decode preview", json.Unmarshal(preview.Body.Bytes(), &got))
	if got.Eligible || got.Reason != string(secretspan.IneligibleAlreadyProtected) {
		t.Fatalf("preview eligible=%v reason=%q, want already_protected", got.Eligible, got.Reason)
	}
	second := f.Post(t, "mark", wire.SecretMarkRequest{
		Range: rng, Name: "Relay token again", Purpose: "Authenticates the staging relay", OperationID: "op-2",
	})
	if second.Code != http.StatusBadRequest {
		t.Fatalf("second mark status = %d, want 400 body=%s", second.Code, second.Body.String())
	}
	if bytes.Contains(second.Body.Bytes(), []byte(contractfixture.RelayToken)) {
		t.Fatal("the refusal carried the selected value")
	}
	list, err := f.Srv.Sources.Workspace.ManagedSecrets.ListProject(t.Context(), f.Project.ID)
	testutil.FailErr(t, "list managed secrets", err)
	if len(list) != 1 {
		t.Fatalf("managed secrets = %d, want the one capability", len(list))
	}
}

func TestMarkRefusesARangeSelectedAgainstAMovedDocument(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	start, end := f.TokenRange()
	w := f.Post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.FixtureDocument.Revision + 5, Start: start, End: end},
		Name:  "Staging relay token", Purpose: "Authenticates the staging relay",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 body=%s", w.Code, w.Body.String())
	}
}

// Marking uses the same minimum value length as screening.

func TestMarkRefusesASelectionBelowTheScreenFloor(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	start, _ := f.TokenRange()
	w := f.Post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.FixtureDocument.Revision, Start: start, End: start + 3},
		Name:  "Short secret", Purpose: "Authenticates a project service",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(contractfixture.RelayToken[:3])) {
		t.Fatal("the refusal carried the selected value")
	}
}

// Preview and marking share the same admission checks.

func TestPreviewRefusesTheSelectionTheMarkWillRefuse(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	start, _ := f.TokenRange()
	rng := wire.SecretMarkRange{Revision: f.FixtureDocument.Revision, Start: start, End: start + 3}

	preview := f.Post(t, "preview", rng)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", preview.Code, preview.Body.String())
	}
	var got wire.SecretMarkPreview
	testutil.FailErr(t, "decode preview", json.Unmarshal(preview.Body.Bytes(), &got))
	if got.Eligible || got.Reason != string(secretspan.IneligibleTooShort) {
		t.Fatalf("preview said eligible=%v reason=%q for a range the mark refuses", got.Eligible, got.Reason)
	}

	mark := f.Post(t, "mark", wire.SecretMarkRequest{
		Range: rng, Name: "Short secret", Purpose: "Authenticates a project service",
	})
	if mark.Code != http.StatusBadRequest {
		t.Fatalf("mark status = %d, want the refusal the preview predicted body=%s", mark.Code, mark.Body.String())
	}
}

func TestMarkRefusesMissingPurpose(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	start, end := f.TokenRange()
	w := f.Post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.FixtureDocument.Revision, Start: start, End: end},
		Name:  "Staging relay token",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400 body=%s", w.Code, w.Body.String())
	}
}
