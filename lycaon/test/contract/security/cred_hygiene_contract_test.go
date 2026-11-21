package contract

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hostidentity"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/webresearch"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Each private file writer leaves owner-only permissions on what it wrote:
// the credential vault and its identity, the daemon manifest, and settings.
func TestSecretFilesDeclare0600(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, cfg)

	vault := credentialstore.Slot{Path: credentialstore.DefaultVaultPath(cfg), Namespace: credentialstore.NamespaceProviders, Context: "providers"}
	store, err := credentialstore.Open(vault, nil)
	contractcheck.FailErr(t, "open credential vault", err)
	contractcheck.FailErr(t, "store a credential", store.Set("fixture", "secret"))
	contractcheck.FailErr(t, "write daemon manifest", hostapi.WriteDaemonManifest("127.0.0.1", 8787, os.Getpid()))
	settingsPath := filepath.Join(cfg, "approvals.yaml")
	approvals, err := settings.NewApprovalStoreAt(settingsPath)
	contractcheck.FailErr(t, "open approval settings", err)
	contractcheck.FailErr(t, "write approval settings", approvals.PutGlobal(settings.ApprovalConfig{}))

	entries, err := os.ReadDir(cfg)
	contractcheck.FailErr(t, "list config dir", err)
	written := map[string]bool{}
	for _, e := range entries {
		written[e.Name()] = true
	}
	for _, name := range []string{credentialstore.VaultBasename, credentialstore.DevelopmentIdentityBasename, "daemon.json", "approvals.yaml"} {
		if !written[name] {
			t.Errorf("%s was not written under the config dir (have %v)", name, entries)
			continue
		}
		info, err := os.Stat(filepath.Join(cfg, name))
		contractcheck.FailErr(t, "stat "+name, err)
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, mode)
		}
	}
}

// Every managed secret namespace resolves to the one vault in the user config
// dir, never under a project tree.
func TestAllManagedSecretNamespacesPersistInVaultAndRegisterSlots(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, cfg)
	wantPath := filepath.Join(cfg, credentialstore.VaultBasename)

	specs := []struct {
		name      string
		fn        func() (credentialstore.Slot, error)
		namespace string
	}{
		{"providers", providercredentials.DefaultSlot, credentialstore.NamespaceProviders},
		{"web research", webresearch.DefaultSlot, credentialstore.NamespaceWebResearch},
		{"MCP OAuth", mcp.DefaultSlot, credentialstore.NamespaceMCPOAuth},
		{"MCP credentials", mcp.DefaultCredentialsSlot, credentialstore.NamespaceMCPCredentials},
		{"host identity", hostidentity.DefaultSlot, credentialstore.NamespaceHostIdentity},
		{"daemon token", hostapi.DefaultDaemonTokenSlot, credentialstore.NamespaceDaemonToken},
	}
	for _, spec := range specs {
		slot, err := spec.fn()
		contractcheck.FailErr(t, spec.name+" slot", err)
		if slot.Path != wantPath {
			t.Errorf("%s vault = %q, want %q", spec.name, slot.Path, wantPath)
		}
		if slot.Namespace != spec.namespace {
			t.Errorf("%s namespace = %q, want %q", spec.name, slot.Namespace, spec.namespace)
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
}
