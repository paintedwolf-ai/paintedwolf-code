package store

import (
	"context"
	"maps"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// contactStore is the surface the contact tests exercise on both stores.
type contactStore interface {
	Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error)
	RecordContact(ctx context.Context, sessionID, host string, contact evidence.Contact) error
	SessionContacts(sessionID string) map[string]evidence.Contact
	SessionUntrustedContent(sessionID string) bool
}

func TestRecordContactBuildsSessionContacts(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		testRecordContactBuildsSessionContacts(t, NewMemory(), "proj-contacts-memory")
	})
	t.Run("sql", func(t *testing.T) {
		sqlDB := testdbfixture.Open(t, "contacts.db")
		testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
		testRecordContactBuildsSessionContacts(t, NewSQL(sqlDB), testdbseed.DefaultProjectID)
	})
}

func testRecordContactBuildsSessionContacts(t *testing.T, st contactStore, projectID string) {
	t.Helper()
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{ProjectID: projectID, Posture: api.SessionPostureBuild}, projectID)
	testutil.FailErr(t, "Create", err)

	testutil.FailErr(t, "RecordContact own", st.RecordContact(ctx, sess.ID, "Example.COM", evidence.ContactOwn))
	testutil.FailErr(t, "RecordContact retrieval", st.RecordContact(ctx, sess.ID, "  Retrieved.Example.Net ", evidence.ContactRetrieval))
	testutil.FailErr(t, "RecordContact audited", st.RecordContact(ctx, sess.ID, "audited.example", evidence.ContactAudited))

	want := map[string]evidence.Contact{
		"example.com":           evidence.ContactOwn,
		"retrieved.example.net": evidence.ContactRetrieval,
		"audited.example":       evidence.ContactAudited,
	}
	if got := st.SessionContacts(sess.ID); !maps.Equal(got, want) {
		t.Fatalf("SessionContacts = %v, want %v", got, want)
	}
	if st.SessionUntrustedContent(sess.ID) {
		t.Fatal("contact records must not mark the session untrusted")
	}

	// The chat's own dial outranks an earlier retrieval contact for the same host.
	testutil.FailErr(t, "RecordContact own after retrieval", st.RecordContact(ctx, sess.ID, "retrieved.example.net", evidence.ContactOwn))
	if got := st.SessionContacts(sess.ID)["retrieved.example.net"]; got != evidence.ContactOwn {
		t.Fatalf("own dial must outrank retrieval, got %v", got)
	}

	// Invalid hosts are silent no-ops.
	testutil.FailErr(t, "RecordContact invalid host", st.RecordContact(ctx, sess.ID, "bad host", evidence.ContactOwn))
	testutil.FailErr(t, "RecordContact empty host", st.RecordContact(ctx, sess.ID, "", evidence.ContactOwn))
	if got := st.SessionContacts(""); got != nil {
		t.Fatalf("SessionContacts(empty) = %v, want nil", got)
	}
	if got := st.SessionContacts("missing-session"); got != nil {
		t.Fatalf("SessionContacts(missing) = %v, want nil", got)
	}
	if err := st.RecordContact(ctx, "missing-session", "example.com", evidence.ContactOwn); err == nil {
		t.Fatal("RecordContact for a missing session must fail")
	}
}

func TestContactRecordShape(t *testing.T) {
	rec, ok := contactRecord("Example.COM", evidence.ContactRetrieval)
	if !ok || rec.Handle != evidence.RetrievedHostHandlePrefix+"example.com" {
		t.Fatalf("retrieval record = %+v ok = %v", rec, ok)
	}
	rec, ok = contactRecord(" Example.COM ", evidence.ContactOwn)
	if !ok || rec.Handle != evidence.DialedHostHandlePrefix+"example.com" {
		t.Fatalf("own record = %+v ok = %v", rec, ok)
	}
	if rec.SourceTool != evidence.DialedHostSourceTool {
		t.Fatalf("source tool = %q, want %q", rec.SourceTool, evidence.DialedHostSourceTool)
	}
	if _, ok := contactRecord("bad host", evidence.ContactOwn); ok {
		t.Fatal("host with a space must be rejected")
	}
	if _, ok := contactRecord("", evidence.ContactNone); ok {
		t.Fatal("empty host must be rejected")
	}
}
