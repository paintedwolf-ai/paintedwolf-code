package approvalstate_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

func TestGateRepeatLedgerDistinctSubjects(t *testing.T) {
	t.Parallel()
	ledger := approvalstate.NewGateRepeatLedger()
	chat := "chat-1"
	key := "authority_misuse:aws-cli/s3-remove-bucket"

	s1 := ledger.NoteAsk(chat, key, "aws s3 rb s3://a")
	if s1.Count != 1 || len(s1.Subjects) != 0 {
		t.Fatalf("first ask = %+v, want count 1 and no prior subjects", s1)
	}

	s2 := ledger.NoteAsk(chat, key, "aws s3 rb s3://b")
	if s2.Count != 2 || len(s2.Subjects) != 1 || s2.Subjects[0] != "aws s3 rb s3://a" {
		t.Fatalf("second ask = %+v", s2)
	}

	s3 := ledger.NoteAsk(chat, key, "aws s3 rb s3://a")
	if s3.Count != 2 {
		t.Fatalf("repeat subject must not increment count: %+v", s3)
	}
}

func TestGateRepeatLedgerSuppressedIncrements(t *testing.T) {
	t.Parallel()
	ledger := approvalstate.NewGateRepeatLedger()
	chat := "chat-1"
	key := "authority_misuse:aws-cli/s3-remove-bucket"

	ledger.NoteAsk(chat, key, "aws s3 rb s3://a")
	ledger.NoteSuppressed(chat, key, "aws s3 rb s3://b")

	s := ledger.NoteAsk(chat, key, "aws s3 rb s3://c")
	if s.Count != 3 {
		t.Fatalf("count = %d want 3", s.Count)
	}
	if s.Suppressed != 1 {
		t.Fatalf("suppressed = %d want 1", s.Suppressed)
	}
}

func TestGateRepeatLedgerUserIntentBoundary(t *testing.T) {
	t.Parallel()
	ledger := approvalstate.NewGateRepeatLedger()
	chat := "chat-1"
	key := "authority_misuse:aws-cli/s3-remove-bucket"

	ledger.NoteAsk(chat, key, "aws s3 rb s3://a")
	ledger.NoteAsk(chat, key, "aws s3 rb s3://b")
	ledger.NoteUserIntentBoundary(chat)

	s := ledger.NoteAsk(chat, key, "aws s3 rb s3://c")
	if s.Count != 1 || len(s.Subjects) != 0 {
		t.Fatalf("after boundary = %+v, want fresh count", s)
	}
}

func TestGateRepeatLedgerSinceLaunchSurvivesBoundary(t *testing.T) {
	t.Parallel()
	ledger := approvalstate.NewGateRepeatLedger()
	chat := "chat-1"
	key := "authority_misuse:aws-cli/s3-remove-bucket"

	ledger.NoteAsk(chat, key, "aws s3 rb s3://a")
	ledger.NoteUserIntentBoundary(chat)
	ledger.NoteAsk(chat, key, "aws s3 rb s3://b")

	counts := ledger.CountsSinceLaunch()
	if counts[key] != 2 {
		t.Fatalf("sinceLaunch[%q] = %d want 2", key, counts[key])
	}
}

func TestGateRepeatLedgerForgetSession(t *testing.T) {
	t.Parallel()
	ledger := approvalstate.NewGateRepeatLedger()
	chat := "chat-1"
	key := "authority_misuse:aws-cli/s3-remove-bucket"

	ledger.NoteAsk(chat, key, "aws s3 rb s3://a")
	ledger.NoteAsk(chat, key, "aws s3 rb s3://b")
	ledger.ForgetSession(chat)

	s := ledger.NoteAsk(chat, key, "aws s3 rb s3://c")
	if s.Count != 1 {
		t.Fatalf("after forget = %+v, want fresh per-chat count", s)
	}
}
