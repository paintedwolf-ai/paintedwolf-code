package review

import (
	"testing"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
)

func TestInventoryShortfallNamesWhatAResubmissionDropped(t *testing.T) {
	m := &Coverage{}
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
