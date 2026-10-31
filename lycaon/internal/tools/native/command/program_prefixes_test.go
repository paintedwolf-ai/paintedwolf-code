package command

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func firstStatement(t *testing.T, source string) commandsurface.Statement {
	t.Helper()
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "catalog memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "program memory", err)
	parsed, err := commandsurface.ParseAdmitted(context.Background(), memory, []byte(source), commandsurface.ParseOptions{})
	testutil.FailErr(t, "parse "+source, err)
	t.Cleanup(func() { _ = parsed.Close() })
	lease, err := parsed.SnapshotWithMemory(context.Background(), memory)
	testutil.FailErr(t, "snapshot "+source, err)
	t.Cleanup(func() { _ = lease.Close() })
	return lease.Plan.Statements[0]
}

func TestNohupOutputRedirectFindsOnlyLiteralNohupOut(t *testing.T) {
	for source, refused := range map[string]bool{
		"nohup ./server > nohup.out":         true,
		"nohup ./server >> ./logs/nohup.out": true,
		"nohup ./server 2> nohup.out":        true,
		"nohup ./server &> nohup.out":        true,
		"nohup ./server > server.log":        false,
		`nohup ./server > "$out"`:            false,
		"nohup ./server < nohup.out":         false,
		"nohup ./server":                     false,
	} {
		redirect, got := nohupOutputRedirect(firstStatement(t, source))
		if got != refused {
			t.Errorf("%q refused = %v, want %v", source, got, refused)
		}
		if got && !strings.Contains(source[redirect.Span.StartByte:redirect.Span.EndByte], "nohup.out") {
			t.Errorf("%q refusal spans %q", source, source[redirect.Span.StartByte:redirect.Span.EndByte])
		}
	}
}

func TestPrefixedCommandIsTheInnerArgv(t *testing.T) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(t, "catalog", err)
	specs := commandIndex(catalog)
	for _, tc := range []struct{ argv, inner string }{
		{"timeout -s KILL -k 1 5 go test ./...", "go test ./..."},
		{"timeout --preserve-status 0.5 sleep -5", "sleep -5"},
		{"timeout 5", ""},
		{"nohup ./server --port 8080", "./server --port 8080"},
		{"nohup -- ./server", "./server"},
	} {
		var argv commandprogram.Fields
		for _, word := range strings.Fields(tc.argv) {
			argv = append(argv, commandprogram.Field{Value: word})
		}
		spec := specs[argv[0].Value]
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, argv)
		testutil.FailErr(t, "bind "+tc.argv, err)
		if got := strings.Join(prefixedCommand(spec.Name, bound).Values(), " "); got != tc.inner {
			t.Errorf("%q inner = %q, want %q", tc.argv, got, tc.inner)
		}
	}
}

func TestEnvironmentMapSplitsAtTheFirstEquals(t *testing.T) {
	got := environmentMap([]string{"A=1", "B=x=y", "C=", "malformed"})
	if len(got) != 3 || got["A"] != "1" || got["B"] != "x=y" || got["C"] != "" {
		t.Fatalf("environment = %v", got)
	}
}
