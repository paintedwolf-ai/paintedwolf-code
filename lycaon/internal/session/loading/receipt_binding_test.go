package loading

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/skills"
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
