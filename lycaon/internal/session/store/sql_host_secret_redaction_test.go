package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestHostSecretRedactionMetadataRoundTrips(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "redaction-meta.db")
	root := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, root)
	s := NewSQL(sqlDB)
	sess, err := s.Create(context.Background(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	msg := api.Message{
		Role: api.MessageRoleTool, Content: "token=[REDACTED]",
		HostSecretRedaction: api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: api.RedactionKindSecret}, {Field: "content", Start: 12, Length: 10, Kind: api.RedactionKindSecret}}),
	}
	testutil.FailErr(t, "append redacted message", s.AppendMessages(context.Background(), sess.ID, msg))
	got, err := s.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(got) != 1 || got[0].HostSecretRedaction == nil || got[0].HostSecretRedaction.Occurrences() != 2 {
		t.Fatalf("round trip = %+v", got)
	}
	patched := got[0]
	patched.HostSecretRedaction = api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: api.RedactionKindSecret}, {Field: "content", Start: 12, Length: 10, Kind: api.RedactionKindSecret}, {Field: "content", Start: 24, Length: 10, Kind: api.RedactionKindSecret}})
	_, err = s.UpdateMessage(context.Background(), sess.ID, patched.ID, patched)
	testutil.FailErr(t, "update redaction metadata", err)
	got, err = s.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "get updated messages", err)
	if got[0].HostSecretRedaction == nil || got[0].HostSecretRedaction.Occurrences() != 3 {
		t.Fatalf("updated round trip = %+v", got[0].HostSecretRedaction)
	}
}
