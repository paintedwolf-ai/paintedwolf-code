package nativegen

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

func TestDiscoveryUsesAdmittedContextAndRetainsRuntime(t *testing.T) {
	provider, runtime := fixtureProvider(t)
	if provider.Profile().Discovery != providerprofile.DiscoveryNativeGeneration {
		t.Fatal("native provider selected network discovery")
	}
	for range 2 {
		entries, err := runtime.DiscoverModels(t.Context())
		if err != nil {
			t.Fatalf("discover admitted model: %v", err)
		}
		if len(entries) != 1 || entries[0].ID != DefaultModelID || entries[0].ContextLength != 64 || entries[0].ContextLengthSource != "native-runtime" {
			t.Fatalf("discovery did not report admitted capacity: %+v", entries)
		}
		if !modelinfo.Supported(entries[0].EffectiveCapabilities().Tools) || runtime.generation != 1 {
			t.Fatal("discovery lost capabilities or reloaded the process")
		}
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if entries, err := runtime.DiscoverModels(t.Context()); len(entries) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("closed runtime advertised a model: %+v, %v", entries, err)
	}
}

func TestDiscoveryRequiresRuntimeAndSuccessfulAdmission(t *testing.T) {
	var absent *Runtime
	if entries, err := absent.DiscoverModels(t.Context()); len(entries) != 0 || err == nil {
		t.Fatalf("missing runtime advertised a model: %+v, %v", entries, err)
	}
	_, runtime := fixtureProvider(t)
	runtime.config.BinaryPath = ""
	if entries, err := runtime.DiscoverModels(t.Context()); len(entries) != 0 || err == nil {
		t.Fatalf("invalid runtime advertised a model: %+v, %v", entries, err)
	}
}

func TestProfileAdvertisesImplementedCacheAndThinkingControls(t *testing.T) {
	profile := New("nativegen", Options{}).Profile()
	if profile.Thinking != modelinfo.ThinkStyleNone || profile.ReasoningWire != providerprofile.ReasoningWireNone || profile.PromptCache.Mode != providerprofile.PromptCacheLocalKV {
		t.Fatalf("native execution profile: %+v", profile)
	}
	if !profile.UtilitySingleFlight {
		t.Fatal("native provider bypassed foreground/background admission")
	}
	entry := DefaultModelEntry()
	if entry.Thinking.State != "unsupported" || entry.Capabilities.Reasoning.State != modelinfo.CapabilityUnsupported {
		t.Fatal("native catalog advertised unsupported reasoning controls")
	}
}
