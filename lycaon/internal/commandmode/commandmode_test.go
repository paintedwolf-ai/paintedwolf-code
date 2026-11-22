package commandmode

import (
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestEveryMultiModeCatalogCommandHasExplicitModeAndUnrecognizedFailsClosed(t *testing.T) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(t, "ReadOnlyLanguageV1", err)
	for _, spec := range catalog.Commands {
		if len(spec.Effects) < 2 {
			continue
		}
		if spec.Name == "sed" {
			got, ok := Resolve(t.Context(), []string{"sed", "p"})
			if !ok || got.Mode != "stream-script" {
				t.Fatalf("sed p = %+v, %v", got, ok)
			}
			gotW, okW := Resolve(t.Context(), []string{"sed", "w out"})
			if !okW || gotW.Mode != "w-command" {
				t.Fatalf("sed 'w out' = %+v, %v", gotW, okW)
			}
			continue
		}
		bound := commandprogram.BoundArgv{Command: spec.Name}
		mode, ok := SelectMultiMode(spec, bound)
		if !ok || !KnownMode(spec.Name, mode) {
			t.Fatalf("multi-mode command %q lacked explicit mode selector (%q, %v)", spec.Name, mode, ok)
		}
	}

	unrecognized := toolcontract.CommandSpecification{
		Name: "unrecognized-multi-mode",
		Effects: []toolcontract.EffectRequirement{
			{Mode: "read-mode", Requires: []string{"read"}},
			{Mode: "write-mode", Requires: []string{"read", "write"}},
		},
	}
	if mode, ok := SelectMultiMode(unrecognized, commandprogram.BoundArgv{Command: unrecognized.Name}); ok || mode != "" {
		t.Fatalf("unrecognized multi-mode command fell back to (%q, %v); must fail closed", mode, ok)
	}
	if KnownMode("sed", "unknown-mode") || KnownMode("unknown-cmd", "always") {
		t.Fatal("KnownMode admitted undeclared mode")
	}
}
