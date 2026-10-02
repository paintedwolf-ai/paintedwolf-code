package secretcap

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// An unlock's row records when it ended and why; a row a stopped engine
// left open closes as restart at the next boot.
func TestUnlockAuditRecordsHowEachUnlockEnded(t *testing.T) {
	service, _, _ := testService(t)
	owner := testOwner(t, service)
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	record := func(id string) presence.Unlock {
		unlock := presence.NewUnlock("root-1", presence.Verified{
			ChallengeID: id, PersonID: owner, Authenticator: presence.AuthenticatorMacOS, WindowLabel: "main", VerifiedAt: at,
		})
		tx, err := service.handle.BeginTx(t.Context(), nil)
		testutil.FailErr(t, "begin", err)
		testutil.FailErr(t, "record unlock", RecordUnlock(t.Context(), tx, testdbseed.DefaultProjectID, unlock))
		testutil.FailErr(t, "commit", tx.Commit())
		return unlock
	}
	ended := record("11111111-1111-4111-8111-111111111111")
	record("22222222-2222-4222-8222-222222222222")

	service.RecordUnlockEnd(t.Context(), ended, presence.EndScreenLocked, at.Add(time.Minute))
	// The first end stands.
	service.RecordUnlockEnd(t.Context(), ended, presence.EndIdle, at.Add(time.Hour))
	testutil.FailErr(t, "close left open", service.CloseUnlocksLeftOpen(t.Context()))

	reasons := map[string]string{}
	rows, err := service.handle.QueryContext(t.Context(), `SELECT id, end_reason FROM vault_unlocks`)
	testutil.FailErr(t, "read unlocks", err)
	defer rows.Close()
	for rows.Next() {
		var id, reason string
		testutil.FailErr(t, "scan unlock", rows.Scan(&id, &reason))
		reasons[id] = reason
	}
	testutil.FailErr(t, "iterate unlocks", rows.Err())
	if reasons["11111111-1111-4111-8111-111111111111"] != string(presence.EndScreenLocked) ||
		reasons["22222222-2222-4222-8222-222222222222"] != string(presence.EndRestart) {
		t.Fatalf("end reasons = %v", reasons)
	}
}
