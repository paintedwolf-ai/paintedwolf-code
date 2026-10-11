package toolexecution

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSecretUseRequiresExplicitHTTPOriginsAndManagedReference(t *testing.T) {
	const reference = "{{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}"
	args := func(service any) map[string]any {
		return map[string]any{"command": "setup --password " + reference, "secret_use": map[string]any{"services": []any{service}}}
	}
	for _, service := range []string{"http://localhost:8080", "https://example.test/", "http://[::1]:9090"} {
		recipients, err := parseSecretUse(catalogContract(t, "command"), args(service))
		testutil.FailErr(t, "parse service permission", err)
		if len(recipients.recipients) != 1 {
			t.Fatal("missing recipient")
		}
	}
	for _, service := range []any{"http://user:pass@example.test", "http://example.test/login", "http://example.test?", "http://example.test#fragment", "ftp://example.test", "http://localhost:0", "http://localhost:65536", reference, 42} {
		if _, err := parseSecretUse(catalogContract(t, "command"), args(service)); err == nil {
			t.Fatalf("accepted invalid service %v", service)
		}
	}
	if _, err := parseSecretUse(catalogContract(t, "write"), args("https://example.test")); err == nil {
		t.Fatal("internal tool declared an outbound permission")
	}
	noReference := args("https://example.test")
	noReference["command"] = "setup"
	if _, err := parseSecretUse(catalogContract(t, "command"), noReference); err == nil {
		t.Fatal("service permission without a managed value")
	} else {
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "SECRET_USE_WITHOUT_REFERENCE" {
			t.Fatalf("missing structured rejection: %v", err)
		}
	}
}

func TestServiceConnectionAuthorityDoesNotDependOnDisplayLabels(t *testing.T) {
	declaration, err := parseSecretUse(catalogContract(t, "command"), map[string]any{
		"command": "setup {{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}",
		"secret_use": map[string]any{"services": []any{
			"https://localhost", "http://[::1]:8080", "http://127.0.0.1:8080", "https://service.test:9090",
		}},
	})
	testutil.FailErr(t, "parse declared service ports", err)
	for i := range declaration.recipients {
		declaration.recipients[i].Label = "Service display name"
	}
	ctx := withSecretUse(t.Context(), declaration)
	if !slices.Equal(secretUseConnectPorts(ctx), []uint16{443, 8080}) {
		t.Fatal("display labels changed the declared local connection authority")
	}
	if argvSecretRecipientsLocal(ctx) {
		t.Fatal("a declared remote service counted as a local recipient")
	}
}

// A process handoff is local unless it names a service beyond loopback.
func TestArgvSecretRecipientsLocalFollowsDeclaredServices(t *testing.T) {
	if !argvSecretRecipientsLocal(t.Context()) {
		t.Fatal("a process handoff without declared services is not local")
	}
	declaration, err := parseSecretUse(catalogContract(t, "command"), map[string]any{
		"command":    "setup {{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}",
		"secret_use": map[string]any{"services": []any{"http://localhost:8080", "http://127.0.0.1:8081", "http://[::1]:9090"}},
	})
	testutil.FailErr(t, "parse loopback services", err)
	if !argvSecretRecipientsLocal(withSecretUse(t.Context(), declaration)) {
		t.Fatal("loopback-only services are not local")
	}
}
