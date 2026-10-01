package contract

import (
	"context"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSecurityFloorForbidsInsecureDemoShortcuts(t *testing.T) {
	t.Parallel()

	out, err := bundledPromptEngine().Render(context.Background(),
		"units/secure-by-default.md", map[string]any{"profile_has_write_tools": true})
	contractcheck.FailErr(t, "render secure-by-default partial", err)

	for _, want := range []string{
		"these rules do not relax for demos, fixtures, tests, or local stacks",
		"every value in a password, API key, signing secret, webhook secret, or token field is a credential",
		"including in dev, demos, fixtures, tests, and local stacks",
		"never use a sample, placeholder, or guessable value",
		"call one \"not real.\"",
		"verify the service rejects missing and incorrect credentials",
		"Security controls stay real",
		"private, internal, or authenticated services make access control a deliverable",
		"Set it explicitly",
		"an insecure shipped or inherited default counts as disabling it",
		"cite both the unauthorized rejection and authorized success",
		"Never weaken, bypass, or leave off authentication, authorization",
		"unable to ship or touch real data",
		"report mock behavior, not security verification",
		"Do not misclassify a floor control as optional",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered security floor missing %q", want)
		}
	}

	// The rules about what a run records and exposes render with the execution tools.
	run, err := bundledPromptEngine().Render(context.Background(), "units/secure-by-default-run.md", map[string]any{})
	contractcheck.FailErr(t, "render secure-by-default-run partial", err)
	for _, want := range []string{
		"Secrets on command lines",
		"recorded verbatim in the session, approval card, and any grant",
		"a value you compose may evade screening",
		"Use a managed secret reference when the host should generate the value",
		"Logging secrets",
		"no `chmod 777`",
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("rendered run security floor missing %q", want)
		}
	}
}
