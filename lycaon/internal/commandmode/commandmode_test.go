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
			got, ok := Resolve(t.Context(), []string{"sed", "p", "file.txt"})
			if !ok || got.Mode != "stream-script" {
				t.Fatalf("sed p = %+v, %v", got, ok)
			}
			gotW, okW := Resolve(t.Context(), []string{"sed", "w out", "file.txt"})
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

func TestSedSharedArgumentInterpretationModes(t *testing.T) {
	cases := []struct {
		words []string
		mode  string
		ok    bool
	}{
		{[]string{"sed", "p", "file.txt"}, "stream-script", true},
		{[]string{"sed", "w out", "file.txt"}, "w-command", true},
		{[]string{"sed", "-i", ".bak", "s/a/b/", "file.txt"}, "-i", true},
		{[]string{"sed", "-i", ".bak", "s/a/b/"}, "", false},
		{[]string{"sed", "-f", "rules.sed", "file.txt"}, "", false},
		{[]string{"sed", "-i", "-f", "rules.sed", "file.txt"}, "-i", true},
		{[]string{"sed", "-i", "-f", "rules.sed"}, "", false},
	}
	for _, tc := range cases {
		got, ok := Resolve(t.Context(), tc.words)
		if ok != tc.ok || got.Mode != tc.mode {
			t.Fatalf("%v: got (%+v, %v), want mode=%q ok=%v", tc.words, got, ok, tc.mode, tc.ok)
		}
	}

	setup, err := sharedSedSetup()
	testutil.FailErr(t, "sharedSedSetup", err)
	mem, err := toolcontract.NewProgramMemory(setup.limits)
	testutil.FailErr(t, "NewProgramMemory", err)
	spec := loadCatalogLookup().specs["sed"]
	fields := commandprogram.Fields{
		{Value: "sed"}, {Value: "-i"}, {Value: ".bak"}, {Value: "s/a/b/"}, {Value: "file.txt"},
	}
	bound, err := (commandprogram.CatalogBinder{}).Bind(spec, fields)
	testutil.FailErr(t, "Bind", err)
	prepared, fail := PrepareSedArguments(t.Context(), bound, fields[0], mem, setup.compiler)
	if fail != nil {
		t.Fatalf("PrepareSedArguments failed: %+v", fail)
	}
	if !prepared.Source.InPlace || prepared.Source.Suffix != ".bak" || prepared.Source.Text != "s/a/b/" || len(prepared.Source.Files) != 1 || prepared.Source.Files[0].Value != "file.txt" {
		t.Fatalf("unexpected PreparedSed.Source: %+v", prepared.Source)
	}
	prepared.Program.Close()
	if mem.InUse().Working != 0 {
		t.Fatalf("working memory leaked after Program.Close: %+v", mem.InUse())
	}
}
