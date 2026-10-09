package contractfixture

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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func AssertProjectSecretScreen(t *testing.T, f SecretSpanFixture, state api.SecretSpanState, reference string) {
	t.Helper()
	screen := f.AwaitScreen(t)
	assert := func(label string, got *api.SecretScreen) {
		t.Helper()
		if got == nil || len(got.Spans) != 1 {
			t.Fatalf("%s screen = %+v", label, got)
		}
		span := got.Spans[0]
		if span.State != state || span.Reference != reference {
			t.Fatalf("%s state/reference = %q/%q", label, span.State, span.Reference)
		}
		raw, err := json.Marshal(got)
		testutil.FailErr(t, "encode projected screen", err)
		if strings.Contains(string(raw), RelayToken) {
			t.Fatalf("%s screen exposed value", label)
		}
	}
	assert("editor", screen)
	side := sourceledger.ComparisonSide{Content: FileWithSecret, Availability: sourceledger.ContentAvailable}
	comparison := sourceledger.Comparison{InRange: true, Before: side, After: side}
	got := f.Srv.Sources.Comparisons.MapSourceComparison(t.Context(), f.Project.ID, comparison)
	assert("history before", got.Before.SecretScreen)
	assert("history after", got.After.SecretScreen)
	other := f.Srv.Sources.Comparisons.MapSourceComparison(t.Context(), uuid.NewString(), comparison)
	if other.Before.SecretScreen == nil || len(other.Before.SecretScreen.Spans) != 0 {
		t.Fatal("project evidence crossed project boundary")
	}
}

var FileWithSecret = "export const staging = {\n  relay: \"" + RelayToken + "\",\n};\n"

func FixtureOwner(t *testing.T, f SecretSpanFixture) string {
	t.Helper()
	owner, err := f.Srv.Sources.Workspace.SessionStore.HostOwner(t.Context())
	testutil.FailErr(t, "read host owner", err)
	return owner.ID
}

func NewSecretSpanFixture(t *testing.T) SecretSpanFixture {
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
	srv := NewTestServer(t, WithSessionStore(store.NewSQL(database)), func(d *hostapi.Dependencies) {
		d.Host.Events = hub
		d.Source.EditorDocuments = editordoc.New(editordoc.NewStore(database), sourceledger.New(database, ""), d.Core.Projects)
		d.Approvals.SecretSpans = secretspan.New(matcher)
		d.Approvals.ManagedSecrets = secretcap.NewWithStore(database, values, matcher.Remember)
		d.Approvals.ApprovalGate = settings.NewRuleApprovalGate(approvals, settings.NoSources())
	})
	t.Cleanup(func() {
		testutil.FailErr(t, "close document service", srv.Sources.Editor.EditorDocuments.Close(context.Background()))
	})

	dir := t.TempDir()
	testutil.FailErr(t, "write fixture",
		os.WriteFile(filepath.Join(dir, "staging.ts"), []byte(FileWithSecret), 0o644))
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	testdbseed.InsertProjectRootWithID(t, database, p.ID, p.Roots[0].ID, dir)
	eventCh, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to editor events", err)
	t.Cleanup(unsubscribe)

	body, err := json.Marshal(api.OpenEditorDocumentRequest{
		Path:   "staging.ts",
		RootID: p.Roots[0].ID, ClientID: "test-client",
	})
	testutil.FailErr(t, "encode open", err)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, NewAuthedRequest(http.MethodPost,
		"/v1/projects/"+p.ID+"/editor-documents", bytes.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("open status = %d body=%s", w.Code, w.Body.String())
	}
	var document api.EditorDocument
	testutil.FailErr(t, "decode document", json.Unmarshal(w.Body.Bytes(), &document))
	return SecretSpanFixture{Srv: srv, Project: p, FixtureDocument: &document, Events: eventCh}
}

// runeRange locates the relay token as the client would: by rune offsets.

const RelayToken = "pw-relay-7QK4-2ZB9-XM31"

// The marked value matches no catalog rule.

type SecretSpanFixture struct {
	Srv             *hostapi.Server
	Project         *project.Project
	FixtureDocument *api.EditorDocument
	Events          <-chan api.EventEnvelope
}

func (f SecretSpanFixture) TokenRange() (int, int) {
	runes := []rune(FileWithSecret)
	target := []rune(RelayToken)
	for i := 0; i+len(target) <= len(runes); i++ {
		if string(runes[i:i+len(target)]) == string(target) {
			return i, i + len(target)
		}
	}
	return -1, -1
}

func (f SecretSpanFixture) Post(t *testing.T, suffix string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	testutil.FailErr(t, "encode body", err)
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, NewAuthedRequest(http.MethodPost,
		"/v1/projects/"+f.Project.ID+"/editor-documents/"+f.FixtureDocument.ID+"/secret-spans/"+suffix,
		bytes.NewReader(raw)))
	return w
}

func (f SecretSpanFixture) AwaitScreen(t *testing.T) *api.SecretScreen {
	t.Helper()
	sum := sha256.Sum256([]byte(FileWithSecret))
	_, evidence, err := secretview.ProjectContext(f.Srv.Sources.Workspace.ManagedSecrets, t.Context(), f.Project.ID)
	testutil.FailErr(t, "snapshot screening evidence", err)
	digest := hex.EncodeToString(sum[:]) + evidence
	var screen *api.SecretScreen
	testutil.WaitFor(t, 2*time.Second, func() bool {
		screen, _ = f.Srv.Sources.Editor.EditorScreen(f.FixtureDocument.ID, digest)
		return screen != nil
	})
	return screen
}
