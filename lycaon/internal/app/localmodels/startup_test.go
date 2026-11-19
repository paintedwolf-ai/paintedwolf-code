package localmodels_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/app/localmodels"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStartupFixturePreservesExplicitDecisionConfiguration(t *testing.T) {
	t.Setenv(bialy.EnvDevice, "cpu")
	t.Setenv(bialy.EnvModelDir, t.TempDir())
	s := localmodels.Start(t.Context(), true)
	if s.Runtime != nil || s.Generation() != nil || s.Issue != nil || s.Decision.Device != "cpu" {
		t.Fatalf("fixture launched or changed configured inference: %+v", s)
	}
	testutil.FailErr(t, "close absent generation", s.CloseGeneration(t.Context()))
	testutil.FailErr(t, "close absent resources", s.CloseResources(t.Context()))
}

func TestStartupFailurePreservesCauseWithoutOfferingRuntimeRepair(t *testing.T) {
	for _, test := range []struct {
		issue error
		state string
	}{
		{fmt.Errorf("allocate: %w", inference.ErrCapacity), "insufficient_memory"},
		{errors.New("inventory unavailable"), "unavailable"},
	} {
		s := localmodels.Startup{Issue: test.issue}
		availability := s.Generation()
		if availability.Runtime != nil || availability.Initial.State != nativegen.ReadinessState(test.state) || availability.Initial.Action != "none" || availability.Initial.Detail != test.issue.Error() {
			t.Fatalf("incorrect startup projection: %+v", availability)
		}
	}
}

func TestInsufficientPhysicalRAMHasCapacityIdentity(t *testing.T) {
	_, err := localmodels.AllocateResources(4<<30, inference.DeviceFacts{}, bialy.MemoryPlan{})
	if !errors.Is(err, inference.ErrCapacity) {
		t.Fatalf("physical RAM failure lost capacity identity: %v", err)
	}
}

func TestInstallerIdentitySurvivesStartupFailures(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		offline, brokenMarker, brokenKeys bool
	}{
		{name: "normal"},
		{name: "offline", offline: true},
		{name: "damaged marker", offline: true, brokenMarker: true},
		{name: "invalid authority", offline: true, brokenKeys: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(configlayout.EnvEngineRoot, root)
			t.Setenv(configdir.EnvConfigDir, t.TempDir())
			generation := filepath.Join(root, "generation")
			testutil.FailErr(t, "create packaged resources", os.Mkdir(generation, 0700))
			marker := filepath.Join(generation, "offline-model")
			tier := ""
			if tc.brokenMarker {
				testutil.FailErr(t, "create unreadable marker", os.Mkdir(marker, 0700))
			} else if tc.offline {
				tier = nativegen.DefaultModelID
				testutil.FailErr(t, "write installer tier", os.WriteFile(marker, []byte(tier+"\n"), 0600))
			}
			if tc.brokenKeys {
				testutil.FailErr(t, "write invalid authority", os.WriteFile(filepath.Join(generation, "release-keys.json"), []byte("{}"), 0600))
			}
			startup := localmodels.Start(t.Context(), false)
			availability := startup.Generation()
			if availability == nil || availability.OfflineInstaller != tc.offline || availability.OfflineTier != tier {
				t.Fatalf("installer identity: %+v", availability)
			}
			if tc.brokenMarker || tc.brokenKeys {
				if startup.Issue == nil || availability.Store != nil {
					t.Fatalf("invalid resources initialized a model store: %+v", availability)
				}
			} else {
				if availability.Store == nil {
					t.Fatal("missing store for first-run model selection")
				}
				fresh, err := availability.Store.AwaitingInitialModel()
				testutil.FailErr(t, "read first-run selection", err)
				if !fresh {
					t.Fatal("startup committed a model choice")
				}
			}
			testutil.FailErr(t, "close generation", startup.CloseGeneration(t.Context()))
			testutil.FailErr(t, "close resources", startup.CloseResources(t.Context()))
		})
	}
}
