package nativegen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/pkg/api"
)

type fixtureAuthority struct{}

func (fixtureAuthority) Verify(modelstore.SignedManifest) error { return nil }

func fixtureProvider(t *testing.T) (*Provider, *Runtime) {
	t.Helper()
	t.Setenv("PW_NATIVEGEN_PROCESS_FIXTURE", "2")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := modelstore.New(t.TempDir(), fixtureAuthority{})
	payload := []byte("fixture weights")
	digest := sha256.Sum256(payload)
	artifact := modelstore.Artifact{Name: "model.gguf", Digest: hex.EncodeToString(digest[:]), Format: "gguf", Bytes: int64(len(payload))}
	if err := store.Stage(bytes.NewReader(payload), artifact); err != nil {
		t.Fatal(err)
	}
	manifest := modelstore.Manifest{Model: DefaultModelID, Revision: "fixture", Device: "vulkan", Artifact: artifact}
	if err := store.Activate(modelstore.SignedManifest{Manifest: manifest, Signature: "fixture"}); err != nil {
		t.Fatal(err)
	}
	devices, _ := inference.NewDevices(8)
	host, _ := inference.NewMemoryBudget(1 << 30)
	device, _ := inference.NewMemoryBudget(1 << 30)
	config := RuntimeConfig{ContextLimit: 64, PrefillChunk: 4, BinaryPath: binary, Backend: "vulkan", PhysicalDevice: inference.PhysicalDevice{Kind: "pci", Address: "0000:03:00.0"}, ModelStore: store, Devices: devices, HostMemory: host, DeviceMemory: device}
	runtime := NewRuntime(config)
	provider := New("fixture", Options{Runtime: runtime})
	t.Cleanup(func() { _ = runtime.Close() })
	return provider, runtime
}

func TestProviderStreamOwnsLeaseAndReservationsUntilClose(t *testing.T) {
	provider, runtime := fixtureProvider(t)
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "What is 2 + 2?"}}, MaxTokens: 2}
	for _, view := range []*Provider{provider, New("second-view", Options{Runtime: runtime})} {
		completion, err := view.Complete(t.Context(), req)
		if err != nil || completion.Content != "4" || completion.Usage.PromptTokens != 3 || completion.Usage.CompletionTokens != 2 || completion.Model != DefaultModelID {
			t.Fatalf("completion = %+v, %v", completion, err)
		}
	}
	if runtime.generation != 1 || runtime.config.HostMemory.Snapshot().ReservedBytes != startupHostBytes || runtime.config.DeviceMemory.Snapshot().ReservedBytes != 256<<20 {
		t.Fatal("loaded model did not retain its process and admitted memory")
	}
	child := runtime.loaded.process
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.done:
	default:
		t.Fatal("close returned before the model process exited")
	}
	if runtime.config.HostMemory.Snapshot().ReservedBytes != 0 || runtime.config.DeviceMemory.Snapshot().ReservedBytes != 0 {
		t.Fatal("close retained model reservations")
	}
	if _, err := provider.Stream(t.Context(), req); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed provider accepted a request: %v", err)
	}
}

func TestProviderCancellationPublishesTerminalErrorWithBackpressure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan modelcall.StreamChunk, 1)
	out <- modelcall.StreamChunk{Content: "buffered"}
	cancel()
	if sendGenerationChunk(ctx, out, modelcall.StreamChunk{ProviderID: "fixture"}) {
		t.Fatal("continued a canceled stream")
	}
	terminal := <-out
	if !terminal.Done || !errors.Is(terminal.Err, context.Canceled) || terminal.ProviderID != "fixture" {
		t.Fatalf("canceled stream omitted its error: %+v", terminal)
	}
}

func TestProviderRefusesCapacityBeforeLoadingModel(t *testing.T) {
	provider, runtime := fixtureProvider(t)
	runtime.config.DeviceMemory, _ = inference.NewMemoryBudget(1)
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}}, MaxTokens: 2}
	if _, err := provider.Stream(t.Context(), req); !errors.Is(err, inference.ErrCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	if runtime.loaded != nil || runtime.config.HostMemory.Snapshot().ReservedBytes != 0 || runtime.config.DeviceMemory.Snapshot().ReservedBytes != 0 {
		t.Fatal("failed admission retained a process or memory")
	}
}

func TestProviderActivationReplacesProcessBetweenRequests(t *testing.T) {
	provider, runtime := fixtureProvider(t)
	request := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}}, MaxTokens: 2}
	if _, err := provider.Complete(t.Context(), request); err != nil {
		t.Fatalf("complete first revision: %v", err)
	}
	previous := runtime.loaded.process
	manifest := previous.lease.Manifest
	manifest.Revision = "replacement"
	if err := runtime.config.ModelStore.Activate(modelstore.SignedManifest{Manifest: manifest, Signature: "fixture"}); err != nil {
		t.Fatalf("activate replacement: %v", err)
	}
	if _, err := provider.Complete(t.Context(), request); err != nil {
		t.Fatalf("complete replacement: %v", err)
	}
	if runtime.generation != 2 || runtime.loaded.process.lease.Manifest.Revision != "replacement" {
		t.Fatal("continued serving the previous artifact revision")
	}
	select {
	case <-previous.done:
	default:
		t.Fatal("replaced the revision without reaping its process")
	}
	if runtime.config.HostMemory.Snapshot().ReservedBytes != startupHostBytes || runtime.config.DeviceMemory.Snapshot().ReservedBytes != 256<<20 {
		t.Fatal("replacement retained both revisions' reservations")
	}
}
