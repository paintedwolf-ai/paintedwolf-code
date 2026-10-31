package llm

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"path/filepath"
	"strings"
	"testing"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
)

// Trust installed by an approval is withdrawn only by that approval's
// operation; trust a person set in Settings survives every operation rollback.
func TestProviderSecretTrustWithdrawalHonorsProvenance(t *testing.T) {
	catalog, registry, _ := trustTestRegistry(t)
	svc := &Service{Catalog: catalog, Registry: registry}
	ctx := context.Background()
	entry, _ := catalog.Get("ollama")
	destination := entry.SecretDestinationID()

	changed, err := svc.TrustProviderForSecrets(ctx, "ollama", destination, "checkpoint-1")
	testutil.FailErr(t, "trust by approval", err)
	if !changed {
		t.Fatal("first trust must change state")
	}
	if entry, _ = catalog.Get("ollama"); !entry.SecretScreenTrusted() || entry.SecretScreenTrustOperation != "checkpoint-1" {
		t.Fatalf("trusted entry = %+v, want trust attributed to checkpoint-1", entry)
	}
	changed, err = svc.WithdrawProviderSecretTrust(ctx, "ollama", destination, "checkpoint-other")
	testutil.FailErr(t, "foreign withdrawal", err)
	if changed {
		t.Fatal("another operation must not withdraw this approval's trust")
	}
	if entry, _ = catalog.Get("ollama"); !entry.SecretScreenTrusted() {
		t.Fatal("foreign withdrawal removed the trust")
	}
	changed, err = svc.WithdrawProviderSecretTrust(ctx, "ollama", destination, "checkpoint-1")
	testutil.FailErr(t, "own withdrawal", err)
	if !changed {
		t.Fatal("the installing operation must be able to withdraw its trust")
	}
	if entry, _ = catalog.Get("ollama"); entry.SecretScreenTrusted() || entry.SecretScreenTrustOperation != "" {
		t.Fatalf("withdrawn entry = %+v", entry)
	}

	// A person's Settings decision carries no operation and outlives any rollback.
	changed, err = svc.TrustProviderForSecrets(ctx, "ollama", destination, "")
	testutil.FailErr(t, "trust by person", err)
	if !changed {
		t.Fatal("person trust must change state")
	}
	if changed, err = svc.TrustProviderForSecrets(ctx, "ollama", destination, "checkpoint-2"); err != nil || changed {
		t.Fatalf("re-trusting an already trusted provider = changed %v err %v", changed, err)
	}
	if entry, _ = catalog.Get("ollama"); entry.SecretScreenTrustOperation != "" {
		t.Fatalf("a later approval must not claim the person's trust: %+v", entry)
	}
	changed, err = svc.WithdrawProviderSecretTrust(ctx, "ollama", destination, "checkpoint-2")
	testutil.FailErr(t, "rollback against person trust", err)
	if changed {
		t.Fatal("an approval rollback must not withdraw trust the person set")
	}
	changed, err = svc.WithdrawProviderSecretTrust(ctx, "ollama", destination, "")
	testutil.FailErr(t, "person withdrawal", err)
	if !changed {
		t.Fatal("the person can always withdraw trust")
	}
	if entry, _ = catalog.Get("ollama"); entry.SecretScreenTrusted() {
		t.Fatal("person withdrawal left the trust in place")
	}
}

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
	registry, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	if err == nil {
		registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
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

// The screen reports composition as a fact and acts on none of it.
func TestModelSecretScreenReportsRequestComposition(t *testing.T) {
	for _, tc := range []struct {
		name        string
		composition modelcall.RequestComposition
		want        bool
	}{
		{name: "conversation", composition: modelcall.CompositionConversation, want: false},
		{name: "host utility", composition: modelcall.CompositionHostUtility, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var finding secretmatch.Alert
			screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
				finding = got
				return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
			})
			req := modelSecretRequest()
			req.Composition = tc.composition
			_, err := screen.Screen(context.Background(), ScreenDestination{ID: "fireworks-main"}, req)
			testutil.FailErr(t, "screen request", err)
			if finding.HostComposed != tc.want {
				t.Fatalf("alert HostComposed = %v, want %v", finding.HostComposed, tc.want)
			}
		})
	}
}

// Utility calls are stamped host-composed at their shared funnel, so a new one
// never inherits the card.
func TestUtilityCallsAreStampedHostComposed(t *testing.T) {
	recorder := &requestRecorder{namedStubProvider: namedStubProvider{id: "lite", content: "summary"}}
	r := newFallbackTestSummarizer(t, recorder, recorder)

	_, err := r.Summarize(context.Background(), "sys", "user prompt", 64)
	testutil.FailErr(t, "Summarize", err)
	if recorder.last.Composition != modelcall.CompositionHostUtility {
		t.Fatalf("composition = %q, want host_utility", recorder.last.Composition)
	}
}

// A utility call carries the session it runs for even when the call site passes
// no identity of its own.
func TestUtilityCallsCarryAmbientSessionAttribution(t *testing.T) {
	recorder := &requestRecorder{namedStubProvider: namedStubProvider{id: "lite", content: "summary"}}
	r := newFallbackTestSummarizer(t, recorder, recorder)
	ctx := curationctx.WithSession(context.Background(), curationctx.Session{
		SessionID: "worker-1", Agent: "implement", ParentSessionID: "chat-1",
		ProjectID: "proj-1", ProjectDir: "/tmp/proj",
	})

	_, err := r.Summarize(ctx, "sys", "user prompt", 64)
	testutil.FailErr(t, "Summarize", err)
	got := recorder.last.Debug
	if got.SessionID != "worker-1" || got.ParentSessionID != "chat-1" ||
		got.ProjectID != "proj-1" || got.ProjectDir != "/tmp/proj" || got.AgentType != "implement" {
		t.Fatalf("attribution = %+v, want the compacting session's identity", got)
	}
}

// Worker compaction uses the parent chat’s screening grants.
func TestCompactionScreensAgainstTheChatItRunsUnder(t *testing.T) {
	var finding secretmatch.Alert
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
		finding = got
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	ctx := curationctx.WithSession(context.Background(), curationctx.Session{
		SessionID: "worker-1", Agent: "implement", ParentSessionID: "chat-1", ProjectID: "proj-1",
	})
	req := modelSecretRequest()
	req.Debug = stampSessionAttribution(modelcall.RequestDebug{Purpose: "compaction"}, curationctx.SessionFrom(ctx))

	_, err := screen.Screen(ctx, ScreenDestination{ID: "fireworks-main"}, req)
	testutil.FailErr(t, "screen a worker compaction", err)
	if finding.SessionID != "worker-1" || finding.RootSessionID != "chat-1" {
		t.Fatalf("alert session = %q root = %q, want worker-1 under chat-1", finding.SessionID, finding.RootSessionID)
	}
}

// A held send is not retried elsewhere: no second destination, no local
// truncation standing in for it.
func TestSummarizeDoesNotFallBackOnAHeldSend(t *testing.T) {
	held := &ModelRequestSecretWithheldError{Guidance: "keep it local"}
	lite := &namedStubProvider{id: "lite", err: held}
	coord := &namedStubProvider{id: "coord", content: "coordinator summary"}
	r := newFallbackTestSummarizer(t, lite, coord)

	got, err := r.Summarize(context.Background(), "sys", "user prompt with a secret", 64)
	if !errors.Is(err, ErrModelRequestSecretWithheld) {
		t.Fatalf("err = %v, want the withheld send", err)
	}
	if got != "" {
		t.Fatalf("summary = %q, want nothing to stand in for a held send", got)
	}
	if coord.calls != 0 {
		t.Fatalf("coordinator calls = %d, want the same bytes not offered to a second destination", coord.calls)
	}
}

// A screen the host could not run is a fault, not a summary: the truncating
// fallback would return a slice of the unscreened prompt.
func TestSummarizeDoesNotTruncateAroundAScreenFault(t *testing.T) {
	fault := &ModelRequestSecretScreenFaultError{Stage: secretmatch.FaultStageCheckpointsUnwired}
	lite := &namedStubProvider{id: "lite", err: fault}
	coord := &namedStubProvider{id: "coord", err: fault}
	r := newFallbackTestSummarizer(t, lite, coord)

	got, err := r.Summarize(context.Background(), "sys", "prompt body "+modelScreenGitHubToken, 64)
	if !errors.Is(err, ErrModelRequestSecretScreenFailed) {
		t.Fatalf("err = %v, want the screen fault", err)
	}
	if strings.Contains(got, modelScreenGitHubToken) {
		t.Fatal("truncate fallback handed back the prompt the screen could not clear")
	}
}

// requestRecorder keeps the last request a provider was handed.
type requestRecorder struct {
	namedStubProvider
	last modelcall.CompletionRequest
}

func (p *requestRecorder) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.last = req
	return p.namedStubProvider.Complete(ctx, req)
}
