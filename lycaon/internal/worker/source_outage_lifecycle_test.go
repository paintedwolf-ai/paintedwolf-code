package worker

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMaterializedWorkerRetriesPromotionAfterSourceOutage(t *testing.T) {
	requireWorkerOutageFixture(t)
	runWorkerLifecycleAcrossProcessRestart(t, false, false)
}

func TestMountedWorkerRetriesPromotionAfterSourceOutage(t *testing.T) {
	requireWorkerOutageFixture(t)
	runWorkerLifecycleAcrossProcessRestart(t, true, false)
}

func requireWorkerOutageFixture(t *testing.T) {
	t.Helper()
	if os.Getenv("PW_M14_WORKER_OUTAGE_CONTROL") == "" {
		t.Skip("requires explicitly provisioned worker source-outage fixture")
	}
	if os.Getenv("PW_M14_LIFECYCLE_SOURCE_PARENT") == "" && os.Getenv("PW_M14_LIFECYCLE_PREPARED_SOURCE") == "" {
		t.Fatal("network source parent or prepared source required for worker outage lifecycle")
	}
}

// Invoked after process recovery and before the ordinary successful promotion.
// The same task and retained checkpoint must remain usable after source failure.
func assertWorkerSourceOutageRetry(t *testing.T, service *MergeService, task *api.WorkerTask, source string) {
	t.Helper()
	control := os.Getenv("PW_M14_WORKER_OUTAGE_CONTROL")
	if control == "" {
		return
	}
	workerOutageMarker(t, control, "armed.json")
	t.Cleanup(func() {
		testutil.FailErr(t, "restore fixture on failure", os.WriteFile(filepath.Join(control, "restore.request"), nil, 0600))
		workerOutageMarker(t, control, "restored.json")
	})
	store, before, published, err := session.OpenTaskCheckpointStore(task)
	testutil.FailErr(t, "open worker checkpoint before source outage", err)
	if !published {
		t.Fatal("recovered worker lacks checkpoint")
	}
	testutil.FailErr(t, "interrupt source transport", os.WriteFile(filepath.Join(control, "stop.request"), nil, 0600))
	workerOutageMarker(t, control, "stopped.json")
	if _, err := os.Stat(filepath.Join(source, "uncached-worker-outage-probe")); err == nil || os.IsNotExist(err) {
		t.Fatalf("source did not report transport failure: %v", err)
	}
	result, err := service.PromoteOverlay(t.Context(), "", task.ID, api.PromoteOverlayInput{})
	if err == nil || result.Status == api.WorkerMergeStatusMerged {
		t.Fatalf("unavailable source accepted worker promotion: result=%+v error=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(control, "restored.json")); !os.IsNotExist(err) {
		t.Fatalf("source restored before failure observation: %v", err)
	}
	after, err := store.LoadManifest(before.CheckpointID)
	testutil.FailErr(t, "read retained worker checkpoint during outage", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed promotion changed checkpoint")
	}
	current, ok := service.Queue.Get(task.ID)
	if !ok || current == nil {
		t.Fatal("failed promotion lost worker task")
	}
	_, reopened, exists, err := session.OpenTaskCheckpointStore(current)
	testutil.FailErr(t, "reopen checkpoint through current task", err)
	if !exists || !reflect.DeepEqual(before, reopened) {
		t.Fatal("failed promotion detached the task checkpoint")
	}
	testutil.FailErr(t, "restore source for worker retry", os.WriteFile(filepath.Join(control, "restore.request"), nil, 0600))
	workerOutageMarker(t, control, "restored.json")
	awaitWorkerSourceReconnect(t, filepath.Join(source, "main.go"))
}

func workerOutageMarker(t *testing.T, control, name string) {
	t.Helper()
	deadline := time.Now().Add(80 * time.Second)
	for {
		_, err := os.Stat(filepath.Join(control, name))
		if err == nil {
			return
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read worker outage marker %s: %v", name, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker outage marker %s not received", name)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A restored transport listener does not mean the kernel has reconnected yet.
// Retry native reads before retrying promotion; never retry unexpected content.
func awaitWorkerSourceReconnect(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			if string(data) != "package main\n" {
				t.Fatalf("source changed during failed promotion: %q", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("source did not reconnect within retry window: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
