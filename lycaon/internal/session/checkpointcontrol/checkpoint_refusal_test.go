package checkpointcontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/people"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptRewindReplaysReceiptWithoutReplacingLaterWork(t *testing.T) {
	r, repository, ledger, id, root := newRewindControlFixture(t)
	owner, err := repository.HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)
	ctx := people.WithCaller(t.Context(), owner)
	anchor := api.Message{ID: uuid.NewString(), Role: api.MessageRoleUser, Content: "restore this turn", Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted}
	testutil.FailErr(t, "append anchor", repository.AppendMessages(ctx, id, anchor))
	path := filepath.Join(root, "file.txt")
	testutil.FailErr(t, "seed file", os.WriteFile(path, []byte("before"), 0o640))
	testutil.FailErr(t, "open capture", r.captures.SealPromptCheckpoint(ctx, id, anchor.ID))
	r.captures.RecordPrimaryMutation(ctx, id, "file.txt")
	testutil.FailErr(t, "write result", os.WriteFile(path, []byte("after"), 0o640))
	project, err := r.rewindProject(ctx, id)
	testutil.FailErr(t, "resolve project", err)
	turn, err := repository.UserTurnOrdinal(ctx, id)
	testutil.FailErr(t, "turn ordinal", err)
	testutil.FailErr(t, "record effect", ledger.Record(ctx, sourceledger.RecordInput{ProjectID: project.ID, RootID: project.Roots[0].ID, Path: "file.txt", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, SessionID: id, Turn: turn, Before: []byte("before"), After: []byte("after")}))
	resets := 0
	r.runtime.ResetWorkers = func(_ context.Context, _ *api.Session, _ string) { resets++ }
	r.runtime.ResetCoordinator = func(context.Context, string) { resets++ }
	r.runtime.ResetTurnLedgers = func(string, string) { resets++ }
	r.runtime.ResetProgress = func(string) { resets++ }
	r.runtime.ResetQueue = func(context.Context, string) { resets++ }
	operation := uuid.NewString()
	result, err := r.RewindToPrompt(ctx, operation, id, anchor.ID, controlPreviewDigest(t, r, ctx, id, anchor.ID))
	testutil.FailErr(t, "rewind prompt", err)
	if result.RestoredPrompt != anchor.Content || result.TruncatedMessageCount != 1 || !reflect.DeepEqual(result.RestoredPaths, []string{"file.txt"}) || resets != 5 {
		t.Fatalf("rewind=%+v resets=%d", result, resets)
	}
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read restored file", err)
	if string(raw) != "before" {
		t.Fatalf("restored bytes=%q", raw)
	}
	messages, err := repository.GetMessages(ctx, id)
	testutil.FailErr(t, "read truncated transcript", err)
	if len(messages) != 0 {
		t.Fatalf("messages retained=%+v", messages)
	}
	testutil.FailErr(t, "write later work", os.WriteFile(path, []byte("later work"), 0o640))
	replay, err := r.RewindToPrompt(ctx, operation, id, anchor.ID, "obsolete-preview")
	testutil.FailErr(t, "replay completed rewind", err)
	if !reflect.DeepEqual(replay, result) || resets != 5 {
		t.Fatalf("replayed=%+v original=%+v resets=%d", replay, result, resets)
	}
	_, err = r.RewindToPrompt(ctx, operation, id, "different-anchor", "")
	if !errors.Is(err, ErrRewindOperationConflict) {
		t.Fatalf("conflicting operation error=%v", err)
	}
	raw, err = os.ReadFile(path)
	testutil.FailErr(t, "read later work", err)
	if string(raw) != "later work" {
		t.Fatalf("replay replaced later bytes=%q", raw)
	}
}

func TestRecoveryResolvesPreparedAndAppliedRewindReceipts(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared rollback", true: "applied completion"}[applied], func(t *testing.T) {
			mgr, journal, path := prepareJournalFixture(t)
			if applied {
				_, err := journal.Apply()
				testutil.FailErr(t, "apply before interruption", err)
			}
			op := sessionstore.RewindOperation{ID: journal.ID, SessionID: journal.TranscriptID, AnchorMessageID: journal.AnchorMessageID, ProjectDir: journal.ProjectDir, JournalPath: journal.Path(), InputDigest: rewindInputDigest(journal.TranscriptID, journal.AnchorMessageID), Status: "prepared"}
			testutil.FailErr(t, "persist interrupted receipt", mgr.store.PrepareRewind(t.Context(), op))
			testutil.FailErr(t, "recover session rewind", mgr.RecoverRewindsForSession(t.Context(), op.SessionID))
			retained, err := mgr.store.GetRewindOperation(t.Context(), op.ID)
			testutil.FailErr(t, "read recovered receipt", err)
			raw, err := os.ReadFile(path)
			testutil.FailErr(t, "read recovered source bytes", err)
			wantStatus, wantBytes := "rolled_back", "after"
			if applied {
				wantStatus, wantBytes = "committed", "before"
			}
			if retained == nil || retained.Status != wantStatus || string(raw) != wantBytes {
				t.Fatalf("recovery receipt=%+v source=%q", retained, raw)
			}
			messages, err := mgr.store.GetMessages(t.Context(), op.SessionID)
			testutil.FailErr(t, "read recovered transcript", err)
			if applied && (len(messages) != 0 || retained.ResponseJSON == "") {
				t.Fatalf("applied recovery lost committed transcript receipt=%+v messages=%+v", retained, messages)
			}
			if !applied && len(messages) != 1 {
				t.Fatalf("rollback truncated original prompt=%+v", messages)
			}
		})
	}
}
