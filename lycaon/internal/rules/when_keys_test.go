package rules

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRuleWhenKeysDerivedFromRegistry(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "NewDefaultRegistry", err)
	testutil.FailErr(t, "RegisterRuleConditions", RegisterRuleConditions(reg))

	want := projectWhenMapKeys(reg)
	got := RuleWhenKeys()
	if len(got) != len(want) {
		t.Fatalf("RuleWhenKeys len = %d want %d\ngot %v\nwant %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RuleWhenKeys[%d] = %q want %q", i, got[i], want[i])
		}
	}
	if !sort.StringsAreSorted(got) {
		t.Fatal("RuleWhenKeys must be sorted")
	}
	hasParent, hasLeaf := false, false
	for _, k := range got {
		if k == "posture_is" {
			hasParent = true
		}
		if strings.HasPrefix(k, "posture_is_") {
			hasLeaf = true
		}
	}
	if !hasParent {
		t.Fatal("RuleWhenKeys missing posture_is map key")
	}
	if hasLeaf {
		t.Fatal("RuleWhenKeys must not list posture_is_* leaf forms")
	}
	for _, k := range got {
		if k == "posture_is" {
			continue
		}
		if !reg.Has(k) {
			t.Fatalf("RuleWhenKeys entry %q is not registered", k)
		}
	}
}
