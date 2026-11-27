package commandprogram_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestCatalogBinderPythonOperandBoundary(t *testing.T) {
	t.Parallel()
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		t.Fatalf("ReadOnlyLanguageV1: %v", err)
	}
	var spec toolcontract.CommandSpecification
	for _, c := range catalog.Commands {
		if c.Name == "python" {
			spec = c
			break
		}
	}
	if spec.Name != "python" {
		t.Fatal("python command missing from language catalog")
	}

	makeFields := func(words ...string) commandprogram.Fields {
		out := make(commandprogram.Fields, len(words))
		for i, w := range words {
			out[i] = commandprogram.Field{Value: w}
		}
		return out
	}

	cases := []struct {
		name         string
		argv         []string
		wantOperands []string
	}{
		{
			name:         "dash c forwards trailing flags verbatim",
			argv:         []string{"python3", "-u", "-c", "import sys; print(sys.argv)", "--unknown-flag", "-x", "1"},
			wantOperands: []string{"--unknown-flag", "-x", "1"},
		},
		{
			name:         "dash m pip list format json",
			argv:         []string{"python3", "-m", "pip", "list", "--format=json"},
			wantOperands: []string{"list", "--format=json"},
		},
		{
			name:         "dash m pytest with flags",
			argv:         []string{"python", "-B", "-m", "pytest", "-q", "--tb=short", "tests/test_a.py"},
			wantOperands: []string{"-q", "--tb=short", "tests/test_a.py"},
		},
		{
			name:         "script path ends option parsing",
			argv:         []string{"python3", "scripts/run.py", "--foo", "-m", "bar"},
			wantOperands: []string{"scripts/run.py", "--foo", "-m", "bar"},
		},
		{
			name:         "stdin marker ends option parsing",
			argv:         []string{"python3", "-", "--arg", "val"},
			wantOperands: []string{"-", "--arg", "val"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bound, err := (commandprogram.CatalogBinder{}).Bind(spec, makeFields(tc.argv...))
			if err != nil {
				t.Fatalf("Bind(%v): %v", tc.argv, err)
			}
			got := bound.Operands().Values()
			if !slices.Equal(got, tc.wantOperands) {
				t.Fatalf("operands = %v, want %v", got, tc.wantOperands)
			}
		})
	}

	// Unknown option BEFORE operand boundary is still rejected.
	if _, err := (commandprogram.CatalogBinder{}).Bind(spec, makeFields("python3", "--not-a-version-1-option")); err == nil {
		t.Fatal("expected unsupported interpreter option before operand boundary to fail")
	}
}
