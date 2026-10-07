package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
)

func TestInventoryIssueDetailsCarryEveryIdAndTheDroppedOnes(t *testing.T) {
	groups := make([]scanfindings.InventoryGroup, 0, inventoryIDLimit+3)
	for i := range inventoryIDLimit + 3 {
		groups = append(groups, scanfindings.InventoryGroup{ID: "group:" + string(rune('a'+i%26)) + string(rune('0'+i/26)), Scanner: "secrets", RuleID: "token", Paths: []string{"src/a.go"}})
	}
	issue := &InventoryIssue{
		ReportDocumentIssue: guidance.ReportDocumentIssue{Code: guidance.ReportInventoryUnaccountedCode, Reason: "r", Count: len(groups)},
		Unaccounted:         groups,
		Regressed:           []string{groups[1].ID},
	}
	details := issue.Details()
	if ids := details["unaccounted_group_ids"].([]string); len(ids) != inventoryIDLimit || ids[0] != groups[0].ID {
		t.Fatalf("ids = %d, want the first %d", len(ids), inventoryIDLimit)
	}
	if details["unaccounted_omitted"] != 3 {
		t.Fatalf("omitted = %v", details["unaccounted_omitted"])
	}
	rows := details["unaccounted_groups"].([]map[string]any)
	if len(rows) != inventoryRowLimit || rows[0]["paths"].([]string)[0] != "src/a.go" {
		t.Fatalf("rows = %+v", rows)
	}
	if regressed := details["regressed_group_ids"].([]string); len(regressed) != 1 || regressed[0] != groups[1].ID {
		t.Fatalf("regressed = %v", regressed)
	}
}

func TestInventoryShortfallNamesWhatAResubmissionDropped(t *testing.T) {
	m := &RunManager{}
	first := []scanfindings.InventoryGroup{{ID: "group:a"}, {ID: "group:b"}}
	if regressed := m.noteInventoryShortfall("run", "challenge", first); regressed != nil {
		t.Fatalf("first submission regressed %v", regressed)
	}
	second := []scanfindings.InventoryGroup{{ID: "group:b"}, {ID: "group:c"}}
	if regressed := m.noteInventoryShortfall("run", "challenge", second); len(regressed) != 1 || regressed[0] != "group:c" {
		t.Fatalf("regressed = %v, want group:c", regressed)
	}
	if regressed := m.noteInventoryShortfall("run", "report", second); regressed != nil {
		t.Fatalf("another phase inherited the memory: %v", regressed)
	}
}
