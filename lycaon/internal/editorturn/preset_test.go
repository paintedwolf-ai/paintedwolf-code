package editorturn

import (
	"testing"

	"github.com/lycaon/lycaon/internal/contribution"
)

func TestPresetBoundaryTableIsClosedAndComplete(t *testing.T) {
	cases := []struct {
		preset  contribution.PresetID
		profile string
		writes  bool
		finding bool
	}{
		{contribution.PresetInspectFile, InspectToolProfileID, false, false},
		{contribution.PresetEditFile, ToolProfileID, true, false},
		{contribution.PresetEditSibling, ToolProfileID, true, false},
		{contribution.PresetFixFinding, ToolProfileID, true, true},
	}
	for _, tc := range cases {
		boundary, ok := PresetBoundaryFor(tc.preset)
		if !ok {
			t.Fatalf("preset %s has no boundary", tc.preset)
		}
		if boundary.ToolProfile != tc.profile || boundary.Writes != tc.writes ||
			boundary.RequiresFinding != tc.finding || !boundary.RequiresTarget {
			t.Fatalf("preset %s boundary = %+v", tc.preset, boundary)
		}
	}
	if _, ok := PresetBoundaryFor("made_up"); ok {
		t.Fatal("unknown presets must have no implementation")
	}
}

func TestPresetWritePins(t *testing.T) {
	edit, _ := PresetBoundaryFor(contribution.PresetEditFile)
	if got := edit.WritePins("pkg/main.go"); len(got) != 1 || got[0] != "pkg/main.go" {
		t.Fatalf("edit_file pins = %v", got)
	}
	sibling, _ := PresetBoundaryFor(contribution.PresetEditSibling)
	if got := sibling.WritePins("pkg/main.go"); len(got) != 2 || got[1] != "pkg/*" {
		t.Fatalf("edit_sibling pins = %v", got)
	}
	inspect, _ := PresetBoundaryFor(contribution.PresetInspectFile)
	if got := inspect.WritePins("pkg/main.go"); got != nil {
		t.Fatalf("inspect_file must never pin writes: %v", got)
	}
}
