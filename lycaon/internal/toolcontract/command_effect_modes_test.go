package toolcontract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A command registers only when every effect mode it declares compiles: a
// mode with no requirements or an unknown one is refused, and a bound child
// set compiles against a stand-in child.
func TestCommandEffectModesCompileAtRegistration(t *testing.T) {
	for name, spec := range map[string]CommandSpecification{
		"no modes":            {Name: "none"},
		"empty requirements":  {Name: "empty", Effects: []EffectRequirement{{Mode: "run"}}},
		"unknown requirement": {Name: "odd", Effects: []EffectRequirement{{Mode: "run", Requires: []string{"teleport"}}}},
	} {
		if err := spec.CompileEffectModes(); err == nil {
			t.Errorf("%s: compiled", name)
		}
	}
	dispatcher := CommandSpecification{Name: "env", Effects: []EffectRequirement{
		{Mode: "inspect", Requires: []string{"read"}},
		{Mode: "dispatch", Requires: []string{"bound-child-effect-set"}},
	}}
	if err := dispatcher.CompileEffectModes(); err != nil {
		t.Fatalf("dispatcher: %v", err)
	}
	catalog, err := ReadOnlyLanguageV1()
	testutil.FailErr(t, "catalog", err)
	for _, spec := range catalog.Commands {
		if err := spec.CompileEffectModes(); err != nil {
			t.Errorf("catalog %s: %v", spec.Name, err)
		}
	}
}
