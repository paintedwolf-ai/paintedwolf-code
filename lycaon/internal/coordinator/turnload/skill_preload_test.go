package turnload

import "testing"

func TestSkillPreloadPersistsUntilTheNextTurn(t *testing.T) {
	ledger := NewLedger()
	skill := &SkillPreload{Name: "test-skill", Score: 3.5, Body: "procedure", Resources: []string{"references/guide.md"}}
	ledger.SetPreload("s", skill)
	skill.Body = "changed"
	skill.Resources[0] = "mutated.md"
	if ledger.Preload("s").Body != "procedure" || ledger.Preload("s").Resources[0] != "references/guide.md" {
		t.Fatal("caller mutation changed the ledger")
	}
	snapshot := ledger.Standing("s")
	restored := NewLedger()
	restored.Restore("s", snapshot, nil)
	snapshot.Skill.Body = "changed"
	snapshot.Skill.Resources[0] = "mutated.md"
	if restored.Preload("s").Body != "procedure" || restored.Preload("s").Resources[0] != "references/guide.md" {
		t.Fatal("recovery did not preserve the rendered procedure")
	}
	restored.BeginTurn("s", TurnOpening{OpeningMessageID: "next"}, Decision{})
	if restored.Preload("s") != nil {
		t.Fatal("skill leaked into a new turn")
	}
	restored.SetPreload("s", skill)
	restored.BeginUndecidedTurn("s", TurnOpening{OpeningMessageID: "undecided"})
	if restored.Preload("s") != nil {
		t.Fatal("skill leaked into an undecided turn")
	}
}
