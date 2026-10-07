package progress

import "testing"

func TestSeedChecklistAppendsValidPendingRows(t *testing.T) {
	content := SeedChecklist("", []string{"leg-1 Auth", "  leg-2   Parser  ", ""})
	if code, _, ok := ValidateAuthorProgress(content); !ok {
		t.Fatalf("seeded checklist invalid: %s", code)
	}
	if _, pending, _ := CloseCounts(content); pending != 2 {
		t.Fatalf("pending rows = %d in %q", pending, content)
	}
	if !AllTerminal(SeedChecklist("# Goal\n\nx\n", nil)) == false {
		t.Fatal("no labels must leave the checklist missing")
	}
}
