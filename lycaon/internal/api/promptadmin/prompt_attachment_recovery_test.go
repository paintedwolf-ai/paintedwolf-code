package promptadmin

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func newPromptAttachmentRecoveryFixture(t *testing.T) (*Handler, *store.Memory, blobstore.Store, *wire.Session) {
	t.Helper()
	sessions := store.NewMemory()
	sess, err := sessions.Create(t.Context(), wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, "project")
	testutil.FailErr(t, "create session", err)
	manager := session.NewHost(sessions, session.Models{Client: nil, Provider: nil, Limits: settings.SessionLimits{}, Cost: nil}, nil)
	manager.SetDataDir(t.TempDir())
	server := &Handler{Deps: Deps{Store: sessions, Sessions: manager}}
	attachmentStore, available := server.AttachmentStore(t.Context(), sess.ProjectID)
	if !available {
		t.Fatal("attachment store unavailable")
	}
	return server, sessions, attachmentStore, sess
}

func admitRetainedAttachment(
	t *testing.T,
	sessions *store.Memory,
	attachmentStore blobstore.Store,
	sess *wire.Session,
) (string, blobstore.Blob) {
	t.Helper()
	blob, err := attachmentStore.Put("notes.txt", strings.NewReader("notes"), bytebound.Materialization(100))
	testutil.FailErr(t, "put attachment", err)
	testutil.FailErr(t, "record attachment", sessions.RecordPromptAttachmentBlob(t.Context(), sess.ProjectID, blob.ID, blob.Size))
	operationID := uuid.NewString()
	_, err = attachmentStore.Retain(operationID, []string{blob.ID})
	testutil.FailErr(t, "retain attachment", err)
	_, _, err = sessions.PutPromptSubmission(t.Context(), store.PromptSubmission{
		ID: operationID, SessionID: sess.ID, ProjectID: sess.ProjectID,
		InputDigest: operationID, InputJSON: `{}`, Origin: store.PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
		AttachmentBlobIDs: []string{blob.ID},
	})
	testutil.FailErr(t, "admit prompt", err)
	testutil.FailErr(t, "release admission guard", attachmentStore.Release(operationID, []string{blob.ID}))
	return operationID, blob
}

func TestPromptAttachmentRetentionsDropFailedAdmission(t *testing.T) {
	server, sessions, attachmentStore, sess := newPromptAttachmentRecoveryFixture(t)
	operationID, blob := admitRetainedAttachment(t, sessions, attachmentStore, sess)
	retentions, err := server.capturePromptAttachmentRetentions(t.Context(), operationID)
	testutil.FailErr(t, "capture attachment retentions", err)
	claimed, won, err := sessions.ClaimPromptSubmission(t.Context(), operationID)
	testutil.FailErr(t, "claim prompt", err)
	if !won {
		t.Fatal("prompt claim lost")
	}
	testutil.FailErr(t, "fail prompt", sessions.FinishPromptSubmission(
		t.Context(), operationID, claimed.ClaimToken, store.PromptSubmissionFailed, "",
		store.PromptSubmissionFailure{Message: "failed"},
	))
	testutil.FailErr(t, "reconcile attachment retentions", server.reconcilePromptAttachmentRetentions(t.Context(), retentions))
	if _, err := attachmentStore.Resolve(blob.ID); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("failed prompt attachment survived: %v", err)
	}
}

func TestPromptAttachmentRetentionsKeepMessageReference(t *testing.T) {
	server, sessions, attachmentStore, sess := newPromptAttachmentRecoveryFixture(t)
	operationID, blob := admitRetainedAttachment(t, sessions, attachmentStore, sess)
	retentions, err := server.capturePromptAttachmentRetentions(t.Context(), operationID)
	testutil.FailErr(t, "capture attachment retentions", err)
	claimed, won, err := sessions.ClaimPromptSubmission(t.Context(), operationID)
	testutil.FailErr(t, "claim prompt", err)
	if !won {
		t.Fatal("prompt claim lost")
	}
	testutil.FailErr(t, "append attachment message", sessions.AppendMessages(t.Context(), sess.ID, wire.Message{
		ID: operationID, Role: wire.MessageRoleUser, Content: "notes",
		ContentParts: []wire.MessageContentPart{{BlobID: blob.ID}},
	}))
	testutil.FailErr(t, "finish prompt", sessions.FinishPromptSubmission(
		t.Context(), operationID, claimed.ClaimToken, store.PromptSubmissionComplete, `{}`, store.PromptSubmissionFailure{},
	))
	testutil.FailErr(t, "reconcile attachment retentions", server.reconcilePromptAttachmentRetentions(t.Context(), retentions))
	_, err = attachmentStore.DiscardStagedBefore(blob.ID, time.Time{})
	testutil.FailErr(t, "attempt to discard message attachment", err)
	if _, err := attachmentStore.Resolve(blob.ID); err != nil {
		t.Fatalf("message attachment was discarded: %v", err)
	}
}

func TestAttachmentStoreUsesDurableRetentionWithoutMarker(t *testing.T) {
	_, sessions, attachmentStore, sess := newPromptAttachmentRecoveryFixture(t)
	_, retained := admitRetainedAttachment(t, sessions, attachmentStore, sess)
	_, err := attachmentStore.DiscardStagedBefore(retained.ID, time.Time{})
	testutil.FailErr(t, "attempt to discard retained attachment", err)
	if _, err := attachmentStore.Resolve(retained.ID); err != nil {
		t.Fatalf("retained attachment was discarded: %v", err)
	}
}
