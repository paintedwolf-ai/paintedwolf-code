package contract

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// secretFileDefinitions pins each secret file and its mode declaration.
var secretFileDefinitions = []struct {
	what     string
	file     string
	modeDecl string
}{
	// Provider, web-research, and MCP OAuth secrets all persist through
	// internal/credentialstore, so one mode declaration covers all three.
	{"every credentialstore namespace", "internal/credentialstore/backend.go", "fileMode = 0o600"},
	{"sidecar bearer token (api.token)", "internal/api/daemon.go", "daemonManifestMode = 0o600"},
	{"settings YAML", "internal/settings/paths.go", "settingsFileMode = 0o600"},
}

func TestSecretFilesDeclare0600(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")

	for _, definition := range secretFileDefinitions {
		content := contractcheck.ReadRepoFile(t, root, definition.file)
		if !strings.Contains(content, definition.modeDecl) {
			t.Fatalf("%s: %s no longer declares %q.\n"+
				"Secret values live user-level at 0600 — a key file at 0644, or one under a project root, is the leak.",
				definition.what, definition.file, definition.modeDecl)
		}
	}
}

// Secrets are never stored primarily under a project tree, where a config
// holding a key can be committed.
func TestSecretStoresResolveUnderUserConfigDir(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")

	for _, rel := range []string{
		"internal/llm/credentials/credentials.go",
		"internal/mcp/oauth_token_store.go",
		"internal/webresearch/credentials.go",
	} {
		content := contractcheck.ReadRepoFile(t, root, rel)
		if strings.Contains(content, "ProjectDir") || strings.Contains(content, settingsoverlay.DirName()+"/") {
			t.Fatalf("%s references a project-scoped path: secret stores resolve under the user config dir only", rel)
		}
	}
}

// One scrubber, not two. Diagnostics export imports this rather than
// re-deriving redaction — the copy that drifts is the one that leaks.
func TestSharedScrubberCoversTheSecretFieldFamily(t *testing.T) {
	t.Parallel()
	mustRedact := []string{
		"api_key", "apiKey", "X-Api-Key", "API-KEY",
		"access_token", "refresh_token", "client_secret",
		"Authorization", "password", "token", "credential", "private_key",
	}
	for _, name := range mustRedact {
		if !observability.IsSecretFieldName(name) {
			t.Fatalf("shared scrubber does not treat %q as a secret field name", name)
		}
	}

	mustKeep := []string{"provider_id", "model", "base_url", "status", "version", "id"}
	for _, name := range mustKeep {
		if observability.IsSecretFieldName(name) {
			t.Fatalf("shared scrubber redacts %q, which carries no secret and is needed to read a diagnostics bundle", name)
		}
	}
}

// The scrubber must survive the shapes a real capture takes: nested objects,
// arrays, headers, and a body that failed to parse.
func TestScrubberRedactsAcrossCaptureShapes(t *testing.T) {
	t.Parallel()
	const key = "sk-live-secret-value-0123456789"

	body := observability.ScrubJSON([]byte(`{"api_key":"` + key + `","nested":{"access_token":"` + key + `"}}`))
	if strings.Contains(string(body), key) {
		t.Fatalf("JSON body leaked a secret: %s", body)
	}

	unparsed := observability.ScrubJSON([]byte("Authorization: Bearer " + key))
	if strings.Contains(string(unparsed), key) {
		t.Fatalf("unparsed body leaked a secret: %s", unparsed)
	}

	headers := observability.ScrubHeaders(map[string][]string{"Authorization": {"Bearer " + key}})
	if strings.Contains(strings.Join(headers["Authorization"], ""), key) {
		t.Fatalf("header leaked a secret: %v", headers)
	}
}

// The approval settings document declares no credential fields, and request
// bodies decode strictly, so a credential never lands in approval settings.
// Free-form rule text is not scanned or classified.
func TestApprovalSettingsDeclareNoCredentialFields(t *testing.T) {
	t.Parallel()
	body := reflect.TypeOf(api.UpdateApprovalsSettingsRequest{})
	for i := 0; i < body.NumField(); i++ {
		name := strings.ToLower(strings.Split(body.Field(i).Tag.Get("json"), ",")[0])
		switch name {
		case "api_key", "secret", "password", "token":
			t.Fatalf("approval settings declare credential field %q", name)
		}
	}
}

// Every settings, MCP, and credential overlay basename is agent policy, so a
// tool's write reaches review and undeclared commands cannot write it.
func TestSettingsOverlaysAreReviewedAgentPolicy(t *testing.T) {
	t.Parallel()
	got := settingsoverlay.SettingsOverlayBasenames()
	if len(got) == 0 {
		t.Fatal("SettingsOverlayBasenames is empty: nothing would be reviewed")
	}

	// The credential-bearing surfaces specifically. Losing any of these means a
	// tool could write a project-local file that later feeds real credentials.
	want := []string{"mcp.yaml", "model-policy.yaml", "approvals.yaml"}
	for _, basename := range want {
		if !slices.Contains(got, basename) {
			t.Fatalf("%q is no longer in SettingsOverlayBasenames: tool writes to it would stop being reviewed", basename)
		}
	}
	for _, basename := range got {
		rel := settingsoverlay.Rel(basename)
		if !slices.ContainsFunc(protectedpath.AgentPolicyLocations(), func(l protectedpath.AgentPolicyLocation) bool { return l.Contains(rel) }) {
			t.Errorf("%s is not an agent-policy location", rel)
		}
	}

	// And the floor must consume the declaration rather than a copy.
	floor := contractcheck.ReadRepoFile(t, filepath.Join(contractcheck.RepoRoot(t), "lycaon"), "internal/confine/agent_policy.go")
	if !strings.Contains(floor, "protectedpath.AgentPolicyLocations()") {
		t.Fatal("internal/confine/agent_policy.go no longer derives its floor from AgentPolicyLocations")
	}
}
