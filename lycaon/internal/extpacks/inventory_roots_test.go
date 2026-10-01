package extpacks

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFixedPathHostYAMLIsNotAUnit(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), EmptyDesired(), nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	for _, id := range []string{
		"host/providers",
		"host/session",
		"host/approvals",
		"host/coordinator-flow",
		"host/anchors/catalog",
		"host/model-policy",
	} {
		if _, ok := eff.Units[id]; ok {
			t.Errorf("%s is read by fixed pack path and must not be published as a unit", id)
		}
	}
}

func TestCrossPackHostFamiliesStayUnits(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), EmptyDesired(), nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	var bindings, notices int
	for id, u := range eff.Loaded {
		switch {
		case strings.HasPrefix(id, "host/bindings/"):
			bindings++
			if u.Kind != "host/bindings" {
				t.Errorf("%s kind = %q want host/bindings", id, u.Kind)
			}
		case strings.HasPrefix(id, "host/user-notices/"):
			notices++
			if u.Kind != "host/user-notices" {
				t.Errorf("%s kind = %q want host/user-notices", id, u.Kind)
			}
		}
	}
	if bindings == 0 {
		t.Error("expected host/bindings units across stock packs")
	}
	if notices == 0 {
		t.Error("expected host/user-notices units")
	}
}

func TestNoScannersUnitKind(t *testing.T) {
	for _, root := range UnitKindRoots() {
		if root == "scanners" {
			t.Fatal("scanners must not be an inventory kind root — packs never ship scanner engines")
		}
	}
	if isUnitID("scanners/foo") {
		t.Error("scanners/foo must not classify as a unit id")
	}
}

func TestIsUnitIDCoversMultiSegmentRoots(t *testing.T) {
	for _, id := range []string{"host/bindings/x", "host/user-notices/session_aborted", "tools/schemas/read"} {
		if !isUnitID(id) {
			t.Errorf("%s must classify as a unit id", id)
		}
	}
	for _, id := range []string{"acme/plan", "host/session", "painted-wolf/platform"} {
		if isUnitID(id) {
			t.Errorf("%s must not classify as a unit id", id)
		}
	}
}
