package editoradmin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/people/peoplestore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	relayToken     = "pw-relay-7QK4-2ZB9-XM31"
	fileWithSecret = "export const staging = {\n  relay: \"" + relayToken + "\",\n};\n"
)

// editorFixture serves the package's handlers on the production route
// patterns, backed by a disposable database and one project root.
type editorFixture struct {
	h          *Handler
	router     chi.Router
	background *taskgroup.Group
	project    *project.Project
	root       string
	sessions   session.Store
	owner      people.Person
	promotions *promotionLog
}

type promotionLog struct {
	mu       sync.Mutex
	projects []string
}

func (l *promotionLog) record(_ context.Context, projectID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.projects = append(l.projects, projectID)
}

func (l *promotionLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.projects)
}

type fixtureOption func(*apitestdeps.Deps)

func withSessions(sessions session.Store) fixtureOption {
	return func(d *apitestdeps.Deps) { d.Store = sessions }
}

func newEditorFixture(t *testing.T, opts ...fixtureOption) *editorFixture {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build secret matcher", err)
	fingerprints, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
	testutil.FailErr(t, "build fingerprinter", err)
	matcher.SetFingerprinter(fingerprints)
	deps := apitestdeps.Deps{Store: store.NewMemory(), Database: testdbfixture.Open(t, "editor.db")}
	for _, opt := range opts {
		opt(&deps)
	}
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "test managed secret",
	}, func(id string) bool { _, err := uuid.Parse(id); return err == nil })
	deps.ManagedSecrets = secretcap.NewWithStore(deps.Database, values, matcher.Remember)
	apitestdeps.Fill(t, &deps)

	promotions := &promotionLog{}
	background := &taskgroup.Group{}
	h := New(&httpio.Responder{Logger: slog.New(slog.DiscardHandler)}, background, Dependencies{
		EditorClients:   editordoc.NewClientLiveness(editordoc.PresenceGrace, nil),
		EditorDocuments: deps.EditorDocuments,
		Events:          deps.Events,
		ManagedSecrets:  deps.ManagedSecrets,
		MutationGate:    deps.MutationGate,
		ProjectRegistry: deps.Projects,
		SecretSpans:     secretspan.New(matcher),
		SessionStore:    deps.Store,
		TryRunPromotion: promotions.record,
	})
	t.Cleanup(func() {
		background.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		background.Wait(ctx)
		testutil.FailErr(t, "drain editor background work", ctx.Err())
	})

	root := t.TempDir()
	testutil.FailErr(t, "write text fixture", os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644))
	testutil.FailErr(t, "write secret fixture", os.WriteFile(filepath.Join(root, "staging.ts"), []byte(fileWithSecret), 0o644))
	p, err := project.CreateWithRoot(t.Context(), deps.Projects, root)
	testutil.FailErr(t, "create project", err)
	owner, err := peoplestore.New(deps.Database).HostOwner(t.Context())
	testutil.FailErr(t, "read host owner", err)
	return &editorFixture{
		h: h, router: editorRouter(h), background: background, project: p, root: root,
		sessions: deps.Store, owner: owner, promotions: promotions,
	}
}

// editorRouter mirrors the editor route patterns in internal/api/routes.go.
func editorRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	base := "/v1/projects/{id}/editor-documents"
	doc := base + "/{document_id}"
	r.Post(base, h.HandleOpenEditorDocument)
	r.Put(base+"/retention", h.HandleReplaceEditorDocumentRetention)
	r.Post(base+"/status", h.HandleReadEditorDocumentStatuses)
	r.Put(doc, h.HandleReplaceEditorDocument)
	r.Post(doc+"/sync", h.HandleSyncEditorDocument)
	r.Post(doc+"/resolve", h.HandleResolveEditorDocumentConflict)
	r.Post(doc+"/snapshots", h.HandleCreateEditorDocumentSnapshot)
	r.Get(doc+"/changes", h.HandleListEditorDocumentChanges)
	r.Post(doc+"/changes/{change_id}/revert", h.HandleRevertEditorDocumentChange)
	r.Post(doc+"/updates", h.HandleSubmitEditorDocumentUpdate)
	r.Post(doc+"/presence", h.HandlePublishEditorDocumentPresence)
	r.Post(doc+"/leave", h.HandleLeaveEditorDocument)
	r.Post(doc+"/discard", h.HandleDiscardEditorDocument)
	r.Post(doc+"/reload", h.HandleReloadEditorDocument)
	r.Post(doc+"/observe", h.HandleObserveEditorDocument)
	r.Post(doc+"/save", h.HandleSaveEditorDocument)
	r.Post(doc+"/secret-spans/preview", h.HandlePreviewEditorSecretMark)
	r.Post(doc+"/secret-spans/mark", h.HandleMarkEditorSecret)
	return r
}

func (f *editorFixture) projectPath(suffix string) string {
	return "/v1/projects/" + f.project.ID + "/editor-documents" + suffix
}

func (f *editorFixture) documentPath(documentID, suffix string) string {
	return f.projectPath("/" + documentID + suffix)
}

// serve sends body as JSON; a string body is sent verbatim.
func (f *editorFixture) serve(t *testing.T, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	switch v := body.(type) {
	case nil:
	case string:
		raw = []byte(v)
	default:
		encoded, err := json.Marshal(v)
		testutil.FailErr(t, "encode request body", err)
		raw = encoded
	}
	request := httptest.NewRequestWithContext(people.WithCaller(t.Context(), f.owner), method, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", httpio.MediaTypeJSON)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

func decodeStatus[T any](t *testing.T, step string, response *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if response.Code != status {
		t.Fatalf("%s status = %d, want %d: %s", step, response.Code, status, response.Body.String())
	}
	var out T
	testutil.FailErr(t, "decode "+step, json.Unmarshal(response.Body.Bytes(), &out))
	return out
}

func expectCode(t *testing.T, step string, response *httptest.ResponseRecorder, code wire.ApiErrorCode) wire.ErrorResponse {
	t.Helper()
	var failure wire.ErrorResponse
	testutil.FailErr(t, "decode "+step+" error", json.Unmarshal(response.Body.Bytes(), &failure))
	if failure.Code != code || response.Code != code.HTTPStatus() {
		t.Fatalf("%s = %d %q, want %d %q", step, response.Code, failure.Code, code.HTTPStatus(), code)
	}
	return failure
}

func (f *editorFixture) open(t *testing.T, path, clientID string) wire.EditorDocument {
	t.Helper()
	response := f.serve(t, http.MethodPost, f.projectPath(""), wire.OpenEditorDocumentRequest{
		Path: path, RootID: f.project.Roots[0].ID, ClientID: clientID,
	})
	return decodeStatus[wire.EditorDocument](t, "open "+path, response, http.StatusCreated)
}

func (f *editorFixture) replace(t *testing.T, d wire.EditorDocument, content string) wire.EditorDocument {
	t.Helper()
	response := f.serve(t, http.MethodPut, f.documentPath(d.ID, ""), wire.ReplaceEditorDocumentRequest{
		OperationID: uuid.NewString(), ClientID: "window", ExpectedRevision: d.Revision, Content: content, EOL: "lf",
	})
	return decodeStatus[wire.EditorDocument](t, "replace draft", response, http.StatusOK)
}

func (f *editorFixture) discard(t *testing.T, d wire.EditorDocument) wire.EditorDocument {
	t.Helper()
	response := f.serve(t, http.MethodPost, f.documentPath(d.ID, "/discard"), wire.EditorDocumentCommandRequest{
		OperationID: uuid.NewString(), ClientID: "window", ExpectedRevision: d.Revision,
	})
	return decodeStatus[wire.EditorDocument](t, "discard draft", response, http.StatusOK)
}

func (f *editorFixture) disk(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, name))
	testutil.FailErr(t, "read "+name, err)
	return string(raw)
}
