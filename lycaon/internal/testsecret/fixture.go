// Package testsecret creates durable capabilities for boundary tests.
package testsecret

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/credentialstore/credentialstoretest"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Fixture binds capabilities to a project, chat, and invoking session.
type Fixture struct {
	Service       *secretcap.Service
	Access        secretcap.ResolveContext
	PersonID      string
	Fingerprinter *secretmatch.Fingerprinter
}

func New(t *testing.T, project, chat, session string) *Fixture {
	t.Helper()
	database := testdbfixture.Open(t, "secrets.db")
	testdbseed.InsertProject(t, database, project)
	testdbseed.InsertSession(t, database, chat, project)
	if session != chat {
		testdbseed.InsertSession(t, database, session, project)
	}
	values := credentialstoretest.Open(t, credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "boundary fixture",
	}, func(id string) bool { _, err := uuid.Parse(id); return err == nil })
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
	testutil.FailErr(t, "create secret fingerprinter", err)
	service := secretcap.NewWithStore(database, values, nil)
	service.SetFingerprinter(fp)
	return &Fixture{Service: service, Access: secretcap.ResolveContext{
		ProjectID: project, ChatSessionID: chat, SessionID: session,
	}, PersonID: testdbseed.OwnerID(t, database), Fingerprinter: fp}
}

// Put records provenance through the capability creation operation.
func (f *Fixture) Put(t *testing.T, name, value, scope, origin string) secretcap.Metadata {
	t.Helper()
	request := secretcap.PutRequest{
		ProjectID: f.Access.ProjectID, ChatSessionID: f.Access.ChatSessionID, SessionID: f.Access.SessionID,
		OperationID: uuid.NewString(), Name: name, Purpose: "boundary test", Value: value,
		Scope: scope, Origin: origin,
	}
	switch origin {
	case secretcap.OriginGenerated:
		request.Format, request.EntropyBits = secretcap.FormatBase64URL, 256
	case secretcap.OriginDetected, secretcap.OriginCookieJar, secretcap.OriginTokenJar:
	default:
		request.PersonID = f.PersonID
	}
	if origin == secretcap.OriginSettingsEntered || origin == secretcap.OriginFileMarked {
		request.SessionID, request.ChatSessionID = "", ""
	}
	result, err := f.Service.Put(t.Context(), request)
	testutil.FailErr(t, "store capability", err)
	return result.Metadata
}

func (f *Fixture) Resolve(t *testing.T, args map[string]any) *secretcap.Resolution {
	t.Helper()
	resolution, err := f.Service.Resolve(t.Context(), args, f.Access)
	testutil.FailErr(t, "resolve capability", err)
	t.Cleanup(func() { resolution.Finish(t.Context()) })
	return resolution
}
