package transcript

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
)

func TestTurnLoadWireCacheReturnsDetachedClone(t *testing.T) {
	t.Parallel()
	receipt := store.TurnLoadReceipt{
		ID:               1,
		SessionID:        "sess-1",
		Trigger:          store.TurnLoadTriggerTurn,
		OpeningMessageID: "msg-1",
		ElapsedMs:        42,
		Decisions:        `{"floor":["edit","read"],"preloaded_skill":{"name":"debug","score":0.9},"turn":{"kind":"code","kind_confidence":0.8,"engine":{"name":"local","model":"m1","head":"h1"}},"boundary":{"cache":"warm","reason":"","idle_ms":10,"cold_after_ms":60000}}`,
		Standing:         `{"tools":[{"tool":"edit","source":"floor","turn":"msg-1","with":"read"}]}`,
	}

	first := TurnLoadWire(receipt)
	if len(first.Floor) != 2 || len(first.Tools) != 1 || first.Engine == nil || first.PreloadedSkill == nil || first.Kind == nil || first.Boundary == nil {
		t.Fatalf("unexpected TurnLoadWire result: %+v", first)
	}
	first.Floor[0] = "mutated"
	first.Tools[0].Tool = "mutated"
	first.Engine.Name = "mutated"
	first.PreloadedSkill.Name = "mutated"
	first.Kind.Value = "mutated"
	first.Boundary.IdleMs = 999

	second := TurnLoadWire(receipt)
	if second.Floor[0] != "edit" || second.Tools[0].Tool != "edit" || second.Engine.Name != "local" || second.PreloadedSkill.Name != "debug" || second.Kind.Value != "code" || second.Boundary.IdleMs != 10 {
		t.Fatalf("cache leaked caller mutation: %+v", second)
	}

	reqReceipt := store.TurnLoadReceipt{
		ID:        2,
		SessionID: "sess-1",
		Trigger:   store.TurnLoadTriggerRequest,
		Abstained: true,
		Decisions: `{"request":{"need":"search","exact":["grep"]}}`,
	}
	reqFirst := TurnLoadWire(reqReceipt)
	if reqFirst.Match == nil || len(reqFirst.Match.Names) != 1 {
		t.Fatalf("unexpected request match: %+v", reqFirst)
	}
	reqFirst.Match.Names[0] = "mutated"
	reqSecond := TurnLoadWire(reqReceipt)
	if reqSecond.Match.Names[0] != "grep" {
		t.Fatalf("request match cache leaked caller mutation: %+v", reqSecond.Match)
	}
}
