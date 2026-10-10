package readiness

import (
	"context"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/userpath"
)

type answeringDecider struct{ decide.Absent }

func (answeringDecider) Available() bool { return true }

func TestPreflightReportsMissingConfiguredRoleProvidersWithoutDiscovery(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	policy := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "removed-coordinator", Model: "fixture"},
		Lite:        llm.ModelRef{ProviderID: "removed-lite", Model: "fixture"},
		AgentPool:   llm.AgentPool{Models: []llm.ModelRef{{ProviderID: "removed-worker", Model: "fixture"}}},
	})
	service := &llm.Service{Registry: &llm.Registry{}, Policy: policy, Utility: llm.NewUtilityPlane()}
	env := New(service, answeringDecider{}, userpath.Snapshot{}).Environment()
	missing := env.MissingRoleProviders()
	if len(missing) != 3 || missing["coordinator"] != "removed-coordinator" || missing["lite"] != "removed-lite" || missing["agent_pool.0"] != "removed-worker" {
		t.Fatalf("missing roles=%v", missing)
	}
	if env.ProviderCount() != 0 || env.CheckDecisionEngine(t.Context()) != "" {
		t.Fatal("readiness invented a provider or rejected the scripted engine")
	}
	if unavailable, detail := env.LiteSlotUnavailable(); unavailable || detail != nil {
		t.Fatalf("unobserved utility state unavailable=%v detail=%v", unavailable, detail)
	}
	empty := New(nil, nil, userpath.Snapshot{}).Environment()
	if empty.ProviderCount() != 0 || empty.MissingRoleProviders() != nil || empty.CheckDecisionEngine(t.Context()) != preflight.ReasonDecisionBinaryMissing {
		t.Fatal("absent runtime was reported ready")
	}
	disabled := New(nil, bialy.New(bialy.Config{Disabled: true}), userpath.Snapshot{}).Environment()
	if disabled.CheckDecisionEngine(t.Context()) != preflight.DecisionReason(bialy.ReasonDisabled) {
		t.Fatal("disabled decision engine lost its structured reason")
	}
}

func TestPreflightMissingBundledExecutablesRefusesReadinessWithoutProvisioning(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LYCAON_ENGINE_ROOT", t.TempDir())
	t.Setenv(browserengine.EnvBrowserBin, "")
	env := New(nil, nil, userpath.Snapshot{}).Environment()
	reason, err := env.CheckBrowser(t.Context())
	if reason != preflight.ReasonBrowserBundleMissing || err == nil {
		t.Fatalf("missing bundled browser=%q,%v", reason, err)
	}
	if path, reason, err := env.ResolveScanner(); path != "" || reason != "not_found" || err == nil {
		t.Fatalf("missing bundled scanner=%q,%q,%v", path, reason, err)
	}
}

type silentUtilityProvider struct{}

func (silentUtilityProvider) ID() string { return "scripted-utility" }
func (silentUtilityProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "scripted-model"}}
}
func (silentUtilityProvider) Profile() providerprofile.Profile { return providerprofile.Ollama() }
func (silentUtilityProvider) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, &failure.ProviderSilentError{ProviderID: "scripted-utility", Model: "scripted-model", Attempts: 1}
}
func (silentUtilityProvider) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, &failure.ProviderSilentError{ProviderID: "scripted-utility", Model: "scripted-model", Attempts: 1}
}

func TestPreflightReflectsObservedUtilityFailureAndRecoveryWithoutChangingProviderInventory(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	t.Cleanup(func() { guidance.SetGuidanceRenderer(nil) })
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	registry := &llm.Registry{}
	testutil.FailErr(t, "register scripted utility transport", registry.Register(silentUtilityProvider{}))
	ref := llm.ModelRef{ProviderID: "scripted-utility", Model: "scripted-model"}
	service := &llm.Service{Registry: registry, Policy: llm.NewInMemoryPolicyStore(llm.ModelPolicy{Coordinator: ref, Lite: ref}), Utility: llm.NewUtilityPlane()}
	probes := New(service, answeringDecider{}, userpath.Snapshot{}).Environment()
	if probes.ProviderCount() != 1 || probes.MissingRoleProviders() != nil {
		t.Fatal("configured utility provider was reported missing")
	}
	summarizer := &llm.RegistrySummarizer{Registry: registry, Policy: service.Policy, Plane: service.Utility, Purpose: "warm_seed", Class: llm.UtilityClassOverlay}
	if _, err := summarizer.SummarizeStreamOnce(t.Context(), "system", "user", 16, nil); err != nil {
		if _, ok := failure.AsProviderSilent(err); !ok {
			t.Fatalf("scripted utility transport did not report silence: %v", err)
		}
	} else {
		t.Fatal("scripted silent utility transport was treated as successful")
	}
	unavailable, detail := probes.LiteSlotUnavailable()
	if !unavailable || detail["provider_id"] != "scripted-utility" || detail["reason"] != "silent" {
		t.Fatalf("readiness lost observed utility failure:%v,%v", unavailable, detail)
	}
	service.Utility.Reset()
	if unavailable, detail := probes.LiteSlotUnavailable(); unavailable || detail != nil {
		t.Fatalf("reset utility remained unavailable:%v,%v", unavailable, detail)
	}
	if probes.ProviderCount() != 1 {
		t.Fatal("observed utility failure removed configured provider identity")
	}
}

func TestPreflightRefusesUnusableExplicitBrowserWithoutProvisioning(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LYCAON_ENGINE_ROOT", t.TempDir())
	directory := t.TempDir()
	binary := filepath.Join(directory, "invalid-browser")
	testutil.FailErr(t, "write unusable browser fixture", os.WriteFile(binary, []byte("invalid executable"), 0700))
	testutil.FailErr(t, "write browser sibling data fixture", os.WriteFile(filepath.Join(directory, "icudtl.dat"), []byte("fixture"), 0600))
	t.Setenv(browserengine.EnvBrowserBin, binary)
	env := New(nil, nil, userpath.Snapshot{}).Environment()
	if reason, err := env.CheckBrowser(t.Context()); reason != preflight.ReasonBrowserUnusable || err == nil {
		t.Fatalf("unusable explicit browser was accepted: %q, %v", reason, err)
	}
}
