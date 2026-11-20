package nativegen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTierRecommendationUsesDeviceAndReservedCapacity(t *testing.T) {
	for _, tc := range []struct {
		physical, available uint64
		recommended         string
	}{
		{4, 4, ""}, {8, 5, DefaultModelID}, {16, 10, DefaultModelID}, {24, 12, tiers[1].id}, {32, 18, tiers[2].id}, {32, 7, DefaultModelID},
	} {
		host, _ := inference.NewMemoryBudget(tc.available << 30)
		device, _ := inference.NewMemoryBudget(tc.available << 30)
		rt := NewRuntime(RuntimeConfig{PhysicalBytes: tc.physical << 30, HostMemory: host, DeviceMemory: device, UnifiedMemory: true})
		rows := rt.ModelTiers()
		var recommended []string
		for _, row := range rows {
			if row.Recommended {
				if !row.Eligible {
					t.Fatal("ineligible recommendation")
				}
				recommended = append(recommended, row.ID)
			}
		}
		if len(recommended) > 1 || len(recommended) == 0 && tc.recommended != "" || len(recommended) == 1 && recommended[0] != tc.recommended {
			t.Fatalf("%+v: %+v", tc, rows)
		}
		testutil.FailErr(t, "close runtime", rt.Close())
	}
}

func TestTierMemoryKindUsesGenerationBackend(t *testing.T) {
	for _, tc := range []struct{ backend, kind string }{{"mlx", "system"}, {"vulkan", "gpu"}, {"", ""}} {
		rt := NewRuntime(RuntimeConfig{Backend: tc.backend})
		var status api.LocalGenerationStatus
		rt.SelectionStatus(&status)
		testutil.FailErr(t, "close runtime", rt.Close())
		if status.MemoryKind != tc.kind {
			t.Fatalf("%s memory kind = %q, want %q", tc.backend, status.MemoryKind, tc.kind)
		}
	}
}

func TestTransferAdmissionAndCancellation(t *testing.T) {
	_, rt := fixtureProvider(t)
	var status api.LocalGenerationStatus
	rt.SelectionStatus(&status)
	if len(status.AvailableModels) != 3 || status.SelectionState != "selected" || status.SelectedModel != DefaultModelID {
		t.Fatalf("installed status: %+v", status)
	}
	req := api.LocalModelSelectionRequest{Model: tiers[1].id}
	settled := make(chan struct{})
	notify := func() { close(settled) }
	if err := rt.StartSelection(req, nil, notify); !errors.Is(err, inference.ErrCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	rt.config.PhysicalBytes = 32 << 30
	rt.config.DeviceMemory, _ = inference.NewMemoryBudget(20 << 30)
	rt.config.Pins = []modelstore.ModelPin{}
	if err := rt.StartSelection(req, nil, notify); err == nil {
		t.Fatal("accepted unpublished tier")
	}
	req.BundlePath = "unknown/relative"
	if err := rt.StartSelection(req, nil, notify); err == nil {
		t.Fatal("accepted relative path")
	}
	_, release, err := rt.acquire(t.Context())
	testutil.FailErr(t, "acquire inference", err)
	req.BundlePath = filepath.Join(t.TempDir(), "model.pwmodel")
	testutil.FailErr(t, "start import", rt.StartSelection(req, nil, notify))
	if err := rt.StartSelection(req, nil, notify); err == nil {
		t.Fatal("started overlapping import")
	}
	rt.SelectionStatus(&status)
	if status.State != "downloading" || status.SelectionState != "selected" || status.TargetModel != req.Model || status.TransferPhase != "preparing" {
		t.Fatalf("transfer status: %+v", status)
	}
	testutil.FailErr(t, "cancel import", rt.CancelTransfer(t.Context()))
	<-settled
	release()
	testutil.FailErr(t, "remove model", rt.RemoveModel(t.Context()))
	status = api.LocalGenerationStatus{}
	rt.SelectionStatus(&status)
	if status.State != "opted_out" || status.SelectionState != "opted_out" || status.SelectedModel != "" {
		t.Fatalf("removed status: %+v", status)
	}
	entries, err := rt.DiscoverModels(t.Context())
	testutil.FailErr(t, "discover empty store", err)
	if len(entries) != 0 {
		t.Fatal("opted-out model remained discoverable")
	}
}

func TestDiscoveryDoesNotProvisionPackagedSeed(t *testing.T) {
	rt := repairFixture(t)
	testutil.FailErr(t, "opt out", rt.RemoveModel(t.Context()))
	if _, err := os.Stat(filepath.Join(rt.config.SeedDirectory, "manifest.json")); err != nil {
		t.Fatal("fixture seed missing")
	}
	entries, err := rt.DiscoverModels(context.Background())
	testutil.FailErr(t, "discover", err)
	if len(entries) != 0 {
		t.Fatal("discovery installed seed")
	}
}

func TestOfflineSelectionLoadsAndDiscoversChosenTier(t *testing.T) {
	rt := repairFixture(t)
	prior := rt.loaded.process
	signed := modelstore.SignedManifest{Manifest: prior.lease.Manifest, Signature: "fixture"}
	signed.Manifest.Model = tiers[1].id
	raw, err := json.Marshal(signed)
	testutil.FailErr(t, "encode tier", err)
	testutil.FailErr(t, "stage tier seed", os.WriteFile(filepath.Join(rt.config.SeedDirectory, "manifest.json"), raw, 0600))
	rt.config.PhysicalBytes = 24 << 30
	rt.config.DeviceMemory, _ = inference.NewMemoryBudget(12 << 30)
	request := api.LocalModelSelectionRequest{Model: tiers[1].id, PackagedSeed: true}
	device, err := rt.config.Devices.Bind(rt.config.PhysicalDevice)
	testutil.FailErr(t, "bind physical device", err)
	permit, err := device.Acquire(t.Context(), inference.Work{Owner: "fixture", Generation: 1, Step: "hold", Priority: inference.Foreground})
	testutil.FailErr(t, "hold device during activation", err)
	t.Cleanup(permit.Complete)
	notified := make(chan struct{})
	testutil.FailErr(t, "start seed import", rt.StartSelection(request, nil, func() { close(notified) }))
	testutil.WaitFor(t, "committed offline installation", func() bool {
		var status api.LocalGenerationStatus
		rt.SelectionStatus(&status)
		return status.SelectedModel == request.Model && status.TransferPhase == "activating"
	})
	testutil.FailErr(t, "cancel after commit", rt.CancelTransfer(t.Context()))
	permit.Complete()
	<-notified
	if err := rt.Warm(t.Context(), DefaultModelID); err == nil || rt.Readiness().State != "ready" {
		t.Fatal("stale request displaced the admitted tier")
	}
	status := api.LocalGenerationStatus{}
	rt.SelectionStatus(&status)
	entries, err := rt.DiscoverModels(t.Context())
	testutil.FailErr(t, "discover selected revision", err)
	if status.SelectedModel != request.Model || status.SeedModel != request.Model || len(entries) != 1 || entries[0].ID != request.Model || rt.loaded.process == prior {
		t.Fatalf("tier switch: %+v %+v", status, entries)
	}
}

func TestOfflineInstallerProvisionsOnlyAPristineStore(t *testing.T) {
	rt := repairFixture(t)
	rt.unload()
	rt.config.ModelStore = modelstore.New(t.TempDir(), fixtureAuthority{})
	rt.config.DeviceMemory, _ = inference.NewMemoryBudget(8 << 30)
	rt.config.PhysicalBytes = 16 << 30
	done := make(chan struct{})
	initialized := ""
	rt.SetupInstaller(DefaultModelID, func(id string) error { initialized = id; return nil }, func() { close(done) })
	<-done
	selection, err := rt.config.ModelStore.Selection()
	testutil.FailErr(t, "read offline selection", err)
	if initialized != DefaultModelID || selection.Model != DefaultModelID || rt.Readiness().State != "ready" {
		t.Fatalf("offline setup: %+v %+v", selection, rt.Readiness())
	}
	testutil.FailErr(t, "opt out", rt.RemoveModel(t.Context()))
	rt.SetupInstaller(DefaultModelID, nil, nil)
	selection, err = rt.config.ModelStore.Selection()
	testutil.FailErr(t, "read retained opt-out", err)
	if selection.State != "opted_out" {
		t.Fatal("installer revoked opt-out")
	}
}

func TestLargeDiscreteTierIncludesDecisionReservation(t *testing.T) {
	const decisionBytes = 3_239_398_502
	const generationBytes = 16_646_471_680
	const required = decisionBytes + generationBytes
	for _, tc := range []struct {
		name               string
		physical, capacity uint64
		eligible           bool
	}{
		{"16 GiB", 16 << 30, 16<<30 - decisionBytes, false},
		{"physical floor", required, generationBytes, true},
		{"below physical floor", required - 1, generationBytes, false},
		{"below generation budget", 24 << 30, generationBytes - 1, false},
		{"Radeon RX 7900 XT", 21_458_059_264, 16_646_579_098, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, _ := inference.NewMemoryBudget(32 << 30)
			device, _ := inference.NewMemoryBudget(tc.capacity)
			rt := NewRuntime(RuntimeConfig{
				Backend: "vulkan", PhysicalBytes: tc.physical, DecisionDeviceBytes: decisionBytes,
				DeviceMemory: device, HostMemory: host,
			})
			t.Cleanup(func() { _ = rt.Close() })
			options := rt.ModelTiers()
			large := options[2]
			if large.MinMemoryBytes != required || large.Eligible != tc.eligible || large.Recommended || !options[1].Recommended {
				t.Fatalf("discrete tier choices: %+v", options)
			}
			if !tc.eligible {
				req := api.LocalModelSelectionRequest{Model: large.ID}
				rt.config.ModelStore = modelstore.New(t.TempDir(), fixtureAuthority{})
				if err := rt.StartSelection(req, nil, nil); !errors.Is(err, inference.ErrCapacity) {
					t.Fatalf("inadmissible selection: %v", err)
				}
			}
		})
	}
}
