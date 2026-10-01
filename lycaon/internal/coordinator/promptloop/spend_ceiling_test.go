package promptloop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTurnCloseoutSpendCeilingReason(t *testing.T) {
	got := promptloop.TurnCloseoutReasonText(promptloop.TurnCloseoutSpendCeiling)
	if got != "the session spend ceiling was reached" {
		t.Fatalf("reason text = %q", got)
	}
}

func TestPromptLoopSpendCeilingMapsToCloseout(t *testing.T) {
	if promptloop.TurnCloseoutSpendCeiling != "session_spend_ceiling" {
		t.Fatalf("closeout reason = %q", promptloop.TurnCloseoutSpendCeiling)
	}
	err := &session.SessionSpendCeilingReached{CeilingUSD: 1, SpentUSD: 1}
	if !errors.Is(err, session.ErrSessionSpendCeiling) {
		t.Fatal("typed ceiling error must unwrap to ErrSessionSpendCeiling")
	}
	_ = context.Background()
	_ = api.Session{}
}
