package loading

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
	"time"
)

type decisionReceiptStore struct {
	Store
	receipts []store.TurnLoadReceipt
}

func (*decisionReceiptStore) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ID: "session"}, nil
}
func (s *decisionReceiptStore) PutTurnLoadReceipt(_ context.Context, r store.TurnLoadReceipt) (store.TurnLoadReceipt, error) {
	s.receipts = append(s.receipts, r)
	return r, nil
}

func TestRequestAndSkillLookupReceiptsRetainCallAndStandingSurface(t *testing.T) {
	repository := &decisionReceiptStore{}
	s := &Service{store: repository, Ledger: turnload.NewLedger()}
	s.Ledger.BeginTurn("session", turnload.TurnOpening{OpeningMessageID: "opening", Request: "Inspect repository", Loadable: []string{"read"}}, turnload.Decision{})
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session", ToolCallID: "call", Agent: "implementer"}, Turn: tools.InvocationTurn{TurnSurfaceID: "implement"}}
	outcome := s.ResolveToolRequest(t.Context(), tctx, "read", []turnload.ToolCard{{Name: "read"}})
	s.RecordToolRequest(t.Context(), tctx, outcome, turnload.RequestToolsResult{Loaded: []string{"read"}}, time.Millisecond)
	s.LookupSkills(t.Context(), tctx, "Review Go code", []skills.Skill{{Name: "review"}})
	if len(repository.receipts) != 2 {
		t.Fatalf("receipts=%+v", repository.receipts)
	}
	for _, receipt := range repository.receipts {
		if receipt.SessionID != "session" || receipt.ToolCallID != "call" || receipt.OpeningMessageID != "opening" || receipt.SurfaceID != "implement" || receipt.Standing == "" {
			t.Fatalf("receipt association=%+v", receipt)
		}
	}
	if repository.receipts[0].Trigger != store.TurnLoadTriggerRequest || !strings.Contains(repository.receipts[0].Decisions, `"loaded":["read"]`) || repository.receipts[1].Trigger != store.TurnLoadTriggerLookup {
		t.Fatalf("receipts=%+v", repository.receipts)
	}
}

func TestContinuationVisibilityRecordsOnlyChangedSurfaces(t *testing.T) {
	repository := &decisionReceiptStore{}
	service := &Service{store: repository, Ledger: turnload.NewLedger()}
	sess := &api.Session{ID: "session"}
	service.Ledger.Restore(sess.ID, turnload.Standing{}, nil)
	service.reconcileVisibility(t.Context(), sess, "opening", "implement", []string{"command"}, []string{"task", "search"}, turnload.RankingUnavailable)
	service.reconcileVisibility(t.Context(), sess, "opening", "implement", []string{"command"}, []string{"task", "search"}, turnload.RankingUnavailable)
	if len(repository.receipts) != 1 {
		t.Fatalf("unchanged continuation duplicated receipts: %+v", repository.receipts)
	}
	receipt := repository.receipts[0]
	if receipt.Trigger != store.TurnLoadTriggerVisibility || receipt.OpeningMessageID != "opening" || !receipt.Abstained || strings.Contains(receipt.Decisions, `"turn"`) {
		t.Fatalf("visibility claimed a turn decision: %+v", receipt)
	}
	var standing turnload.Standing
	testutil.FailErr(t, "decode visibility", json.Unmarshal([]byte(receipt.Standing), &standing))
	restored := turnload.NewLedger()
	restored.Restore(sess.ID, standing, nil)
	if !restored.FullCatalog(sess.ID) || len(restored.Active(sess.ID)) != 2 || len(restored.Needed(sess.ID)) != 0 {
		t.Fatalf("restored visibility = %+v", restored.Standing(sess.ID))
	}
	service.reconcileVisibility(t.Context(), sess, "opening", "implement", []string{"command"}, []string{"task"}, "")
	if len(repository.receipts) != 2 || service.Ledger.Active(sess.ID)["search"] {
		t.Fatal("scope restriction was not recorded")
	}
}
