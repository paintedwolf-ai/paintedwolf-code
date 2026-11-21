package policyfacts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerMintRule struct{}

func (workerMintRule) MintedCredentialRule(action hitl.ProposedAction) (hitl.DetectionMatch, bool) {
	return hitl.DetectionMatch{RuleID: "fixture.issue", RuleTitle: "Issue test credential"}, action.Scope.SessionID == "worker" && action.Invocation.Tool == "command"
}

func TestMintedWorkerOutputIsRememberedInItsParentChat(t *testing.T) {
	db := testdbfixture.Open(t, "mint.db")
	for _, id := range []string{"root", "worker", "other"} {
		testdbseed.InsertSession(t, db, id, testdbseed.DefaultProjectID)
	}
	_, err := db.ExecContext(t.Context(), "UPDATE sessions SET parent_session_id = 'root', agent_type = 'implementer' WHERE id = 'worker'")
	testutil.FailErr(t, "attach worker", err)
	host := session.NewHost(store.NewSQL(db), session.Models{Limits: settings.DefaultSessionLimits()}, nil)
	host.ToolPolicy.SetMintedCredentialSource(func() policyfacts.MintedCredentialSource { return workerMintRule{} })
	const value = "0123456789abcdef0123456789abcdef01234567"
	calls := 0
	host.ToolPolicy.SetRememberSecrets(func(root string, values []secretmatch.Remembered) {
		calls++
		if root != "root" || len(values) != 1 || values[0].Secret != value || values[0].RuleID != "fixture.issue" {
			t.Errorf("unexpected scope or provenance for remembered token in %q", root)
		}
	})
	for _, id := range []string{"other", "worker"} {
		host.ToolPolicy.ObserveMintedCredential(t.Context(), &api.Session{ID: id}, "command", nil, "Access token was successfully created: "+value)
	}
	if calls != 1 {
		t.Fatalf("remembered output %d times", calls)
	}
}
