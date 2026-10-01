package settings_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAskQuietDayExpiresAndChatSurvives(t *testing.T) {
	t.Parallel()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStore", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())

	day, created := gate.PutAskQuiet(hitl.AskQuiet{
		ChatSessionID: "chat-1",
		Key:           "authority_misuse:aws-cli/s3-remove-bucket",
		Label:         "aws-cli / s3-remove-bucket",
	}, hitl.DayRungTTLSeconds)
	if !created || day.ID == "" || day.ExpiresAt == nil {
		t.Fatalf("day quiet = %+v created=%v", day, created)
	}
	if _, ok := gate.AskQuietLive("chat-1", day.Key); !ok {
		t.Fatal("day quiet should be live")
	}

	chat, created := gate.PutAskQuiet(hitl.AskQuiet{
		ChatSessionID: "chat-1",
		Key:           "secret_outbound:aws-access-token\x00seam:fetch\x00host:api.example.com",
		Label:         "aws-access-token → api.example.com",
	}, 0)
	if !created || chat.ExpiresAt != nil {
		t.Fatalf("chat quiet = %+v created=%v", chat, created)
	}

	gate.NoteAskQuietSuppressed("chat-1", chat.Key)
	listed := gate.ListAskQuiets("chat-1")
	if len(listed) != 2 {
		t.Fatalf("list = %d", len(listed))
	}
	var suppressed int
	for _, q := range listed {
		if q.ID == chat.ID {
			suppressed = q.Suppressed
		}
	}
	if suppressed != 1 {
		t.Fatalf("suppressed = %d", suppressed)
	}

	if !gate.RevokeAskQuiet(day.ID) {
		t.Fatal("revoke day")
	}
	if _, ok := gate.AskQuietLive("chat-1", day.Key); ok {
		t.Fatal("revoked day should be gone")
	}
	if _, ok := gate.AskQuietLive("chat-1", chat.Key); !ok {
		t.Fatal("chat quiet should remain")
	}

	gate.ForgetSession("chat-1")
	if len(gate.ListAskQuiets("chat-1")) != 0 {
		t.Fatal("ForgetSession must clear quiets")
	}
}

func TestAskQuietActiveDuplicatePreservesOwnerAndExpiry(t *testing.T) {
	t.Parallel()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStore", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	first, _ := gate.PutAskQuiet(hitl.AskQuiet{
		ChatSessionID: "chat", Key: "authority_misuse:pack/rule", Label: "first", OwnerOperationID: "operation-1",
	}, 60)
	second, created := gate.PutAskQuiet(hitl.AskQuiet{
		ChatSessionID: "chat", Key: "authority_misuse:pack/rule", Label: "second", OwnerOperationID: "operation-2",
	}, 60)
	if created {
		t.Fatal("active duplicate should not report created")
	}
	if first.ID != second.ID {
		t.Fatalf("id drift %q vs %q", first.ID, second.ID)
	}
	if second.Label != "first" || second.OwnerOperationID != "operation-1" {
		t.Fatalf("active authority was overwritten: %+v", second)
	}
	if first.ExpiresAt == nil || second.ExpiresAt == nil || !first.ExpiresAt.Equal(*second.ExpiresAt) {
		t.Fatalf("active expiry changed: first=%+v second=%+v", first, second)
	}
	if gate.RevokeAskQuietInstalledBy(first.ID, "operation-2") {
		t.Fatal("foreign operation revoked quiet")
	}
	if _, ok := gate.AskQuietLive("chat", first.Key); !ok {
		t.Fatal("foreign rollback removed live quiet")
	}
	if !gate.RevokeAskQuietInstalledBy(first.ID, "operation-1") {
		t.Fatal("installing operation could not revoke quiet")
	}
	reissued, created := gate.PutAskQuiet(hitl.AskQuiet{
		ChatSessionID: "chat", Key: first.Key, Label: "reissued", OwnerOperationID: "operation-2",
	}, 60)
	if !created || reissued.OwnerOperationID != "operation-2" || reissued.ExpiresAt == nil || !reissued.ExpiresAt.After(time.Now()) {
		t.Fatalf("reissue = %+v created=%v", reissued, created)
	}
}
