package llm

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

var errStopAtScreen = errors.New("stop at screen")

// destinationCapturingScreen records the destination the registry resolved
// and stops the request before any driver runs.
type destinationCapturingScreen struct {
	destination ScreenDestination
	calls       int
}

func (s *destinationCapturingScreen) Screen(_ context.Context, destination ScreenDestination, _ modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
	s.destination = destination
	s.calls++
	return modelcall.CompletionRequest{}, errStopAtScreen
}

const trustTestShipYAML = `providers:
  - id: ollama
    base_url: http://localhost:11434/v1
    api_key_env: ""
    models:
      - id: llama3.1
` + MinimalShipHTTPRetryYAML

func trustTestRegistry(t *testing.T) (*ProviderCatalog, *Registry, *destinationCapturingScreen) {
	t.Helper()
	catalog := mustCatalogCloneShipToLocal(t, trustTestShipYAML)
	registry, err := NewRegistry(catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	testutil.FailErr(t, "NewRegistry", err)
	screen := &destinationCapturingScreen{}
	registry.SetOutboundSecretScreen(screen)
	return catalog, registry, screen
}

func screenedDestination(t *testing.T, registry *Registry, id string) ScreenDestination {
	t.Helper()
	provider, err := registry.Get(id)
	testutil.FailErr(t, "registry.Get", err)
	screen := registry.outboundSecretScreen.(*destinationCapturingScreen)
	if _, err := provider.Complete(context.Background(), modelcall.CompletionRequest{}); !errors.Is(err, errStopAtScreen) {
		t.Fatalf("Complete err = %v, want the screen to be reached first", err)
	}
	return screen.destination
}

// Trust is stored as the destination identity it was granted against and holds
// only while the instance still resolves to that identity.
func TestProviderSecretScreenTrustBindsToTheResolvedDestination(t *testing.T) {
	catalog, registry, _ := trustTestRegistry(t)
	entry, ok := catalog.Get("ollama")
	if !ok || entry.SecretScreenTrusted() {
		t.Fatalf("fresh instance = %+v (found %v), want untrusted", entry, ok)
	}
	before := screenedDestination(t, registry, "ollama")
	if before.Trusted || before.ID != entry.SecretDestinationID() || before.Label != "ollama" {
		t.Fatalf("untrusted screen destination = %+v", before)
	}

	resolved, err := catalog.ResolveEntry(ProviderEntry{
		ID: "ollama", BaseURL: "http://localhost:11434/v1", Models: []modelinfo.Entry{{ID: "llama3.1"}},
	})
	testutil.FailErr(t, "ResolveEntry", err)
	if resolved.SecretDestinationID() != entry.SecretDestinationID() {
		t.Fatalf("ResolveEntry destination %q differs from the loaded %q", resolved.SecretDestinationID(), entry.SecretDestinationID())
	}

	testutil.FailErr(t, "Put trusted", catalog.Put(ProviderEntry{
		ID: "ollama", BaseURL: "http://localhost:11434/v1", Models: []modelinfo.Entry{{ID: "llama3.1"}},
		SecretScreenTrust: resolved.SecretDestinationID(),
	}))
	testutil.FailErr(t, "Reload catalog", catalog.Reload())
	testutil.FailErr(t, "Reload registry", registry.Reload(context.Background()))
	entry, _ = catalog.Get("ollama")
	if !entry.SecretScreenTrusted() {
		t.Fatalf("stored trust %q did not match destination %q", entry.SecretScreenTrust, entry.SecretDestinationID())
	}
	trusted := screenedDestination(t, registry, "ollama")
	if !trusted.Trusted || trusted.ID != entry.SecretDestinationID() {
		t.Fatalf("trusted screen destination = %+v", trusted)
	}
	if list := registry.List(context.Background()); len(list) != 1 || !list[0].SecretScreenTrusted {
		t.Fatalf("List projection = %+v, want secret_screen_trusted", list)
	}

	// Repointing keeps the stored string and withdraws the trust it named.
	testutil.FailErr(t, "Put repointed", catalog.Put(ProviderEntry{
		ID: "ollama", BaseURL: "http://localhost:11435/v1", Models: []modelinfo.Entry{{ID: "llama3.1"}},
		SecretScreenTrust: resolved.SecretDestinationID(),
	}))
	testutil.FailErr(t, "Reload catalog", catalog.Reload())
	testutil.FailErr(t, "Reload registry", registry.Reload(context.Background()))
	entry, _ = catalog.Get("ollama")
	if entry.SecretScreenTrusted() || entry.SecretScreenTrust == "" {
		t.Fatalf("repointed instance = %+v, want stored trust that no longer applies", entry)
	}
	repointed := screenedDestination(t, registry, "ollama")
	if repointed.Trusted || repointed.ID == trusted.ID {
		t.Fatalf("repointed screen destination = %+v, want a new untrusted identity", repointed)
	}
	if list := registry.List(context.Background()); len(list) != 1 || list[0].SecretScreenTrusted {
		t.Fatalf("List projection = %+v, want trust withdrawn", list)
	}
}
