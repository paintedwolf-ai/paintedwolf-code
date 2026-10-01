package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const relayToken = "pw-relay-7QK4-2ZB9-XM31"

// The marked value matches no catalog rule.
var fileWithSecret = "export const staging = {\n  relay: \"" + relayToken + "\",\n};\n"

type secretSpanFixture struct {
	srv      *Server
	project  *project.Project
	document *wire.EditorDocument
	events   <-chan wire.EventEnvelope
}

func newSecretSpanFixture(t *testing.T) secretSpanFixture {
	t.Helper()
	hub := events.NewMemoryHub()
	database := testdbfixture.Open(t, "editor.db")
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	matcher.SetFingerprinter(fp)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "test managed secret",
	},
		func(id string) bool { _, err := uuid.Parse(id); return err == nil },
	)
	approvals, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "approval store", err)
	srv := newTestServer(t, withSessionStore(sessionstore.NewSQL(database)), func(d *Dependencies) {
		d.Events = hub
		d.EditorDocuments = editordoc.New(editordoc.NewStore(database), sourceledger.New(database, ""), d.Projects)
		d.SecretSpans = secretspan.New(matcher)
		d.ManagedSecrets = secretcap.NewWithStore(database, values, matcher.Remember)
		d.ApprovalGate = settings.NewRuleApprovalGate(approvals, settings.NoSources())
	})
	t.Cleanup(func() {
		testutil.FailErr(t, "close document service", srv.Sources.EditorDocuments.Close(context.Background()))
	})

	dir := t.TempDir()
	testutil.FailErr(t, "write fixture",
		os.WriteFile(filepath.Join(dir, "staging.ts"), []byte(fileWithSecret), 0o644))
	p, err := project.CreateWithRoot(t.Context(), srv.projectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	testdbseed.InsertProjectRootWithID(t, database, p.ID, p.Roots[0].ID, dir)
	eventCh, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to editor events", err)
	t.Cleanup(unsubscribe)

	body, err := json.Marshal(wire.OpenEditorDocumentRequest{
		Path:   "staging.ts",
		RootID: p.Roots[0].ID, ClientID: "test-client",
	})
	testutil.FailErr(t, "encode open", err)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, newAuthedRequest(http.MethodPost,
		"/v1/projects/"+p.ID+"/editor-documents", bytes.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("open status = %d body=%s", w.Code, w.Body.String())
	}
	var document wire.EditorDocument
	testutil.FailErr(t, "decode document", json.Unmarshal(w.Body.Bytes(), &document))
	return secretSpanFixture{srv: srv, project: p, document: &document, events: eventCh}
}

// runeRange locates the relay token as the client would: by rune offsets.
func (f secretSpanFixture) tokenRange() (int, int) {
	runes := []rune(fileWithSecret)
	target := []rune(relayToken)
	for i := 0; i+len(target) <= len(runes); i++ {
		if string(runes[i:i+len(target)]) == string(target) {
			return i, i + len(target)
		}
	}
	return -1, -1
}

func (f secretSpanFixture) post(t *testing.T, suffix string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	testutil.FailErr(t, "encode body", err)
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, newAuthedRequest(http.MethodPost,
		"/v1/projects/"+f.project.ID+"/editor-documents/"+f.document.ID+"/secret-spans/"+suffix,
		bytes.NewReader(raw)))
	return w
}

func (f secretSpanFixture) awaitScreen(t *testing.T) *wire.SecretScreen {
	t.Helper()
	sum := sha256.Sum256([]byte(fileWithSecret))
	_, evidence, err := secretview.ProjectContext(f.srv.Sources.ManagedSecrets, t.Context(), f.project.ID)
	testutil.FailErr(t, "snapshot screening evidence", err)
	digest := hex.EncodeToString(sum[:]) + evidence
	var screen *wire.SecretScreen
	testutil.WaitFor(t, 2*time.Second, func() bool {
		screen, _ = f.srv.Sources.EditorScreen(f.document.ID, digest)
		return screen != nil
	})
	return screen
}

func TestOpenEditorDocumentDoesNotWaitForSecretScreen(t *testing.T) {
	f := newSecretSpanFixture(t)
	if f.document.SecretScreenStatus != wire.SecretScreenPending {
		t.Fatalf("screen status = %q, want pending", f.document.SecretScreenStatus)
	}
	if f.document.SecretScreen != nil {
		t.Fatal("an open response must not wait for or carry the background screen")
	}
	screen := f.awaitScreen(t)
	if screen.ScreenedRevision != f.document.Revision {
		t.Fatalf("screened revision = %d, document revision = %d",
			screen.ScreenedRevision, f.document.Revision)
	}
	if screen.CatalogVersion == "" {
		t.Fatal("a screen that ran must name its catalog")
	}
}

func TestCompletedSecretScreenPublishesAContentFreeTransition(t *testing.T) {
	f := newSecretSpanFixture(t)
	envelope := testutil.Receive(t, "completed screen event", f.events)
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
	f := newSecretSpanFixture(t)
	body, err := json.Marshal(wire.LeaveEditorDocumentRequest{ClientID: "test-client"})
	testutil.FailErr(t, "encode release", err)
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, newAuthedRequest(
		http.MethodPost,
		"/v1/projects/"+f.project.ID+"/editor-documents/"+f.document.ID+"/leave",
		bytes.NewReader(body),
	))

	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("release response = status %d body %q", w.Code, w.Body.String())
	}
}

func TestCachedSecretScreenProjectsOntoANewerMetadataRevision(t *testing.T) {
	f := newSecretSpanFixture(t)
	_ = f.awaitScreen(t)
	document, err := f.srv.Sources.EditorDocuments.CurrentSnapshot(t.Context(), f.project.ID, f.document.ID)
	testutil.FailErr(t, "load document", err)
	document.Revision++
	status, screen := f.srv.Sources.EditorScreenProjection(t.Context(), document)
	if status != wire.SecretScreenComplete || screen == nil {
		t.Fatalf("screen projection = status %q screen %v", status, screen)
	}
	if screen.ScreenedRevision != document.Revision {
		t.Fatalf("screened revision = %d, want %d", screen.ScreenedRevision, document.Revision)
	}
}

func TestScreenCarriesNoSecretBytesOnTheWire(t *testing.T) {
	f := newSecretSpanFixture(t)
	raw, err := json.Marshal(f.awaitScreen(t))
	testutil.FailErr(t, "encode screen", err)
	if bytes.Contains(raw, []byte(relayToken)) {
		t.Fatalf("the screen carried the value: %s", raw)
	}
}

func TestPreviewTrimsQuotesAndReportsWhatItWouldCapture(t *testing.T) {
	f := newSecretSpanFixture(t)
	start, end := f.tokenRange()
	// Select including both surrounding quotes, as a person would.
	w := f.post(t, "preview", wire.SecretMarkRange{
		Revision: f.document.Revision, Start: start - 1, End: end + 1,
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
	if bytes.Contains(w.Body.Bytes(), []byte(relayToken)) {
		t.Fatal("the preview echoed the value back")
	}
}

func TestMarkStoresTheCapabilityAndLeavesTheFileAlone(t *testing.T) {
	f := newSecretSpanFixture(t)
	start, end := f.tokenRange()
	before, err := os.ReadFile(filepath.Join(f.project.Roots[0].Path, "staging.ts"))
	testutil.FailErr(t, "read before", err)

	w := f.post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.document.Revision, Start: start, End: end},
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
	if bytes.Contains(w.Body.Bytes(), []byte(relayToken)) {
		t.Fatal("the mint response carried the value")
	}

	after, err := os.ReadFile(filepath.Join(f.project.Roots[0].Path, "staging.ts"))
	testutil.FailErr(t, "read after", err)
	if !bytes.Equal(before, after) {
		t.Fatal("marking must not modify the file")
	}
}

func TestMarkingTheSameRangeTwiceReturnsOneCapability(t *testing.T) {
	f := newSecretSpanFixture(t)
	start, end := f.tokenRange()
	request := wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.document.Revision, Start: start, End: end},
		Name:  "Staging relay token", Purpose: "Authenticates the staging relay", OperationID: "op-" + uuid.NewString(),
	}
	first := f.post(t, "mark", request)
	second := f.post(t, "mark", request)
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
	f := newSecretSpanFixture(t)
	start, end := f.tokenRange()
	first := f.post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.document.Revision, Start: start, End: end},
		Name:  "Staging relay token", Purpose: "Authenticates the staging relay", OperationID: "op-1",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first mark status = %d body=%s", first.Code, first.Body.String())
	}
	// A wider selection still holds the protected bytes.
	rng := wire.SecretMarkRange{Revision: f.document.Revision, Start: start - 1, End: end + 1, Trim: new(bool)}
	preview := f.post(t, "preview", rng)
	var got wire.SecretMarkPreview
	testutil.FailErr(t, "decode preview", json.Unmarshal(preview.Body.Bytes(), &got))
	if got.Eligible || got.Reason != string(secretspan.IneligibleAlreadyProtected) {
		t.Fatalf("preview eligible=%v reason=%q, want already_protected", got.Eligible, got.Reason)
	}
	second := f.post(t, "mark", wire.SecretMarkRequest{
		Range: rng, Name: "Relay token again", Purpose: "Authenticates the staging relay", OperationID: "op-2",
	})
	if second.Code != http.StatusBadRequest {
		t.Fatalf("second mark status = %d, want 400 body=%s", second.Code, second.Body.String())
	}
	if bytes.Contains(second.Body.Bytes(), []byte(relayToken)) {
		t.Fatal("the refusal carried the selected value")
	}
	list, err := f.srv.Sources.ManagedSecrets.ListProject(t.Context(), f.project.ID)
	testutil.FailErr(t, "list managed secrets", err)
	if len(list) != 1 {
		t.Fatalf("managed secrets = %d, want the one capability", len(list))
	}
}

func TestMarkRefusesARangeSelectedAgainstAMovedDocument(t *testing.T) {
	f := newSecretSpanFixture(t)
	start, end := f.tokenRange()
	w := f.post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.document.Revision + 5, Start: start, End: end},
		Name:  "Staging relay token", Purpose: "Authenticates the staging relay",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 body=%s", w.Code, w.Body.String())
	}
}

// Marking uses the same minimum value length as screening.
func TestMarkRefusesASelectionBelowTheScreenFloor(t *testing.T) {
	f := newSecretSpanFixture(t)
	start, _ := f.tokenRange()
	w := f.post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.document.Revision, Start: start, End: start + 3},
		Name:  "Short secret", Purpose: "Authenticates a project service",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(relayToken[:3])) {
		t.Fatal("the refusal carried the selected value")
	}
}

// Preview and marking share the same admission checks.
func TestPreviewRefusesTheSelectionTheMarkWillRefuse(t *testing.T) {
	f := newSecretSpanFixture(t)
	start, _ := f.tokenRange()
	rng := wire.SecretMarkRange{Revision: f.document.Revision, Start: start, End: start + 3}

	preview := f.post(t, "preview", rng)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", preview.Code, preview.Body.String())
	}
	var got wire.SecretMarkPreview
	testutil.FailErr(t, "decode preview", json.Unmarshal(preview.Body.Bytes(), &got))
	if got.Eligible || got.Reason != string(secretspan.IneligibleTooShort) {
		t.Fatalf("preview said eligible=%v reason=%q for a range the mark refuses", got.Eligible, got.Reason)
	}

	mark := f.post(t, "mark", wire.SecretMarkRequest{
		Range: rng, Name: "Short secret", Purpose: "Authenticates a project service",
	})
	if mark.Code != http.StatusBadRequest {
		t.Fatalf("mark status = %d, want the refusal the preview predicted body=%s", mark.Code, mark.Body.String())
	}
}

func TestMarkRefusesMissingPurpose(t *testing.T) {
	f := newSecretSpanFixture(t)
	start, end := f.tokenRange()
	w := f.post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.document.Revision, Start: start, End: end},
		Name:  "Staging relay token",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400 body=%s", w.Code, w.Body.String())
	}
}
