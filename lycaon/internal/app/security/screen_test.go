package security

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"testing"
)

type screenAuthorityFixture struct {
	ctx            context.Context
	alert          secretmatch.Alert
	asked, unasked int
	failure        error
}

func (a *screenAuthorityFixture) ResolveSecretScreenUnasked(ctx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
	a.ctx, a.alert = ctx, alert
	a.unasked++
	return secretmatch.Resolution{}, a.failure
}
func (a *screenAuthorityFixture) AskSecretScreen(ctx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
	a.ctx, a.alert = ctx, alert
	a.asked++
	return secretmatch.Resolution{}, a.failure
}

func TestSecretScreenPreservesRecordedDecisionsWhenApprovalsDisabled(t *testing.T) {
	runtime := &Runtime{}
	ctx := secretmatch.WithAskAttribution(t.Context(), secretmatch.AskAttribution{ProjectDir: "/fixture-project"})
	alert := secretmatch.Alert{SessionID: "chat", RootSessionID: "root", RuleID: "fixture-secret"}
	refusal := errors.New("recorded screen refused delivery")
	authority := &screenAuthorityFixture{failure: refusal}
	disabled := true
	ask := runtime.Ask(authority, func(root string) bool {
		if root != "/fixture-project" {
			t.Fatalf("screen attributed to %q", root)
		}
		return disabled
	})
	if _, err := ask(ctx, alert); !errors.Is(err, refusal) {
		t.Fatalf("recorded refusal discarded: %v", err)
	}
	if authority.unasked != 1 || authority.asked != 0 || authority.ctx != ctx || authority.alert.SessionID != alert.SessionID {
		t.Fatal("disabled approvals bypassed recorded screen decisions or changed attribution")
	}
	disabled = false
	if _, err := ask(ctx, alert); !errors.Is(err, refusal) {
		t.Fatalf("interactive refusal discarded: %v", err)
	}
	if authority.asked != 1 || authority.unasked != 1 {
		t.Fatal("interactive screening used the unattended path")
	}
	if _, err := runtime.Ask(nil, func(string) bool { return true })(ctx, alert); err == nil {
		t.Fatal("missing screen authority admitted secret delivery")
	}
}
