package editordoc

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHumanPublicationKeepsContributionsFromMultipleChats(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "base\n"})
	first := divergedDocumentHoldingAnAgentEdit(t, f, "a.txt", "typed first\n")
	input := agentEdit(first.Document, "typed first\nsecond\n")
	input.SessionID, input.ToolCallID = "chat-2", "second-call"
	second, err := f.service.ApplyAgentEdit(t.Context(), input)
	testutil.FailErr(t, "accept second chat", err)
	f.write(t, "a.txt", "base\n")
	observed, err := f.service.ObserveDisk(t.Context(), f.project, second.Document.ID, "window")
	testutil.FailErr(t, "restore editable disk representation", err)
	operation := uuid.NewString()
	saved, err := f.service.Save(t.Context(), f.project, observed.ID, "window", operation, "human-chat", 7, observed.Revision)
	testutil.FailErr(t, "publish shared document", err)
	if saved.Draft != "typed first\nsecond\n" || saved.Dirty || f.disk(t, "a.txt") != saved.Draft {
		t.Fatalf("publication changed shared text: %+v", saved)
	}
	var sharedEffect string
	for _, sessionID := range []string{"chat-1", "chat-2"} {
		walk, err := ledger.Walk.QueryWalk(t.Context(), f.project.ID, sourceledger.Baseline{Kind: sourceledger.BaselineSession,
			SessionID: sessionID, WithoutUserEdits: true}, 100, 0, sourceledger.CommitLens{})
		testutil.FailErr(t, "review contributing chat", err)
		if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 || walk.Files[0].UnpresentedAgentEffects != 1 {
			t.Fatalf("chat %s lost shared publication: %+v", sessionID, walk)
		}
		effect := walk.Files[0].Effects[0]
		if sharedEffect != "" && effect.ID != sharedEffect {
			t.Fatal("one publication became multiple effects")
		}
		sharedEffect = effect.ID
		agents := map[string]bool{}
		for _, author := range effect.Contributors {
			if author.Origin == "agent" {
				agents[author.SessionID] = true
			}
		}
		if !agents["chat-1"] || !agents["chat-2"] {
			t.Fatalf("review lost contributors: %+v", effect.Contributors)
		}
	}
	rows, err := f.store.db.QueryContext(t.Context(), `SELECT DISTINCT c.session_id FROM source_effect_contributions ec
 JOIN source_text_contributions c ON c.id=ec.contribution_id
 JOIN source_effects e ON e.id=ec.effect_id
 JOIN source_operations o ON o.id=e.operation_id
 WHERE o.operation_key=? AND c.origin='agent' ORDER BY c.session_id`, operation)
	testutil.FailErr(t, "read publication contributors", err)
	defer func() { testutil.FailErr(t, "close contributors", rows.Close()) }()
	var sessions []string
	for rows.Next() {
		var session string
		testutil.FailErr(t, "read contributor", rows.Scan(&session))
		sessions = append(sessions, session)
	}
	testutil.FailErr(t, "enumerate contributors", rows.Err())
	if len(sessions) != 2 || sessions[0] != "chat-1" || sessions[1] != "chat-2" {
		t.Fatalf("human save flattened chat authorship: %v", sessions)
	}
}
