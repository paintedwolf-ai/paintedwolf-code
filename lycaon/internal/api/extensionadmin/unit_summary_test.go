package extensionadmin

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExtensionUnitContributionArrayContract(t *testing.T) {
	for _, status := range []extpacks.UnitStatus{
		extpacks.UnitStatusLoaded, extpacks.UnitStatusDisabled,
		extpacks.UnitStatusConflict, extpacks.UnitStatusOwned,
	} {
		for _, bodies := range []bool{false, true} {
			u := unitSummaryWire(extpacks.UnitEffective{ID: "guidance/example", Status: status}, bodies)
			data, err := json.Marshal(u)
			testutil.FailErr(t, "marshal empty unit", err)
			var fields map[string]json.RawMessage
			testutil.FailErr(t, "decode empty unit", json.Unmarshal(data, &fields))
			if string(fields["contributions"]) != "[]" {
				t.Fatalf("status=%s bodies=%t contributions=%s", status, bodies, fields["contributions"])
			}
		}
	}
}

func TestExtensionUnitWireCarriesProjectDisableCapability(t *testing.T) {
	for _, kind := range append(extpacks.UnitKindRoots(), "unknown/future-kind") {
		unit := extpacks.UnitEffective{ID: kind + "/fixture", Kind: kind,
			Contributions: []extpacks.UnitContribution{{PackID: "fixture/pack"}}}
		for _, bodies := range []bool{false, true} {
			got := unitSummaryWire(unit, bodies)
			if got.ProjectDisableAllowed != extpacks.ProjectDisableAllowed(kind, true) {
				t.Fatalf("kind=%s bodies=%t capability disagrees with host floor", kind, bodies)
			}
			data, err := json.Marshal(got)
			testutil.FailErr(t, "marshal unit capability", err)
			var fields map[string]json.RawMessage
			testutil.FailErr(t, "decode unit capability", json.Unmarshal(data, &fields))
			if fields["project_disable_allowed"] == nil {
				t.Fatalf("kind=%s omitted capability", kind)
			}
		}
	}
}
