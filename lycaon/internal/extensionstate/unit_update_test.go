package extensionstate_test

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnitUpdateRejectsBothFieldsTogether(t *testing.T) {
	owner := testOwner(t)
	unit := stockUnitIDAt(t, 0)
	before := currentRevision(t, owner, "")
	enabled := false
	_, err := owner.Apply(context.Background(), extensionstate.Intent{
		Scope: deviceScope(), ExpectedRevision: before,
		Op: extensionstate.UpdateUnitOp{UnitID: unit, Enabled: &enabled, OwnSet: true, OwnPackID: "missing-pack"},
	})
	if err == nil {
		t.Fatal("invalid ownership must reject the complete update")
	}
	if got := currentRevision(t, owner, ""); got != before {
		t.Fatalf("rejected update changed revision: %s -> %s", before, got)
	}
	path, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	desired, err := extpacks.LoadDesiredFile(path)
	testutil.FailErr(t, "load desired state", err)
	if slices.Contains(desired.Disabled, unit) {
		t.Fatal("rejected ownership update committed enablement")
	}
}

func TestUnitUpdateCommitsBothFieldsTogether(t *testing.T) {
	owner := testOwner(t)
	unit := stockUnitIDAt(t, 0)
	enabled := false
	result := apply(t, owner, deviceScope(), extensionstate.UpdateUnitOp{
		UnitID: unit, Enabled: &enabled, OwnSet: true,
	})
	if !slices.Contains(result.Desired.Disabled, unit) {
		t.Fatal("combined update must commit disablement")
	}
	if _, ok := result.Desired.Own[unit]; ok {
		t.Fatal("combined update must clear ownership")
	}
	enabled = true
	result = apply(t, owner, deviceScope(), extensionstate.UpdateUnitOp{UnitID: unit, Enabled: &enabled})
	if slices.Contains(result.Desired.Disabled, unit) {
		t.Fatal("enablement update must clear disablement")
	}
}
