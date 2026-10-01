package main

import (
	"context"
	"strings"
	"testing"
)

func TestParseExtFlagsRejectsRemovedScopeFlag(t *testing.T) {
	_, _, err := parseExtFlags([]string{"--scope", "workspace"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseExtFlagsRejectsConflictingScopeSelectors(t *testing.T) {
	_, _, err := parseExtFlags([]string{"--device", "--project", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "context may be set once") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunExtValidateRejectsPositionalArgument(t *testing.T) {
	err := runExtValidate(t.Context(), []string{"extra"})
	if err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("err=%v", err)
	}
}

func TestExtensionCommandsRejectUnusedFlags(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{name: "remove json", run: func() error { return runExtRemove(context.Background(), []string{"acme/x", "--json"}) }, want: "--json"},
		{name: "remove version", run: func() error { return runExtRemove(context.Background(), []string{"acme/x", "--version", "^1.0.0"}) }, want: "--version"},
		{name: "install project", run: func() error {
			return runExtInstall(context.Background(), []string{"path:pack", "--project", t.TempDir()})
		}, want: "--project"},
		{name: "project pack disable", run: func() error { return runExtDisable(context.Background(), []string{"acme/x", "--project", t.TempDir()}) }, want: "units, not packs"},
		{name: "validate ref", run: func() error { return runExtValidate(t.Context(), []string{"--ref", "main"}) }, want: "--ref"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestInspectCommandRejectsInvalidInputBeforeCatalogIO(t *testing.T) {
	for name, args := range map[string][]string{
		"missing id":       {},
		"invalid id":       {"not-an-id"},
		"unsupported flag": {"acme/pack:command", "--version", "1.0.0"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := runExtInspectCommand(t.Context(), args); err == nil {
				t.Fatal("invalid inspect invocation accepted")
			}
		})
	}
}
