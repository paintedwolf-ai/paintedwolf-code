package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestMessageProvenanceRoundTrips(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	store := NewSQL(sqlDB)
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{
		Posture: api.SessionPostureBuild, ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	want := api.Message{
		ID: "mixed", Role: api.MessageRoleUser, Content: "inspect\nfile",
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
		ContentParts: []api.MessageContentPart{
			{Content: "inspect", Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted},
			{Content: "file", Origin: api.MessageOriginAttachment, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Source: "attachment", MediaType: "text/plain"},
		},
	}
	testutil.FailErr(t, "append", store.AppendMessages(context.Background(), sess.ID, want))
	got, err := store.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(got) != 1 || got[0].Origin != want.Origin || got[0].Authority != want.Authority ||
		got[0].TrustTier != want.TrustTier || len(got[0].ContentParts) != 2 {
		t.Fatalf("round trip = %+v", got)
	}
	part := got[0].ContentParts[1]
	if part.Origin != api.MessageOriginAttachment || part.Authority != api.ContentAuthorityNone ||
		part.TrustTier != api.ContentTrustTierUntrusted ||
		part.Source != "attachment" || part.MediaType != "text/plain" {
		t.Fatalf("part = %+v", part)
	}
}
