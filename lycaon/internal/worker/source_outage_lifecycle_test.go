//go:build fieldfixture && darwin

package worker

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/sourceoutage"
	"github.com/lycaon/lycaon/pkg/api"
)

func init() { sourceOutageRetry = assertWorkerSourceOutageRetry }

func TestMaterializedWorkerRetriesPromotionAfterSourceOutage(t *testing.T) {
	runWorkerLifecycleOnSource(t, false, false, outageSource(t))
}

func TestMountedWorkerRetriesPromotionAfterSourceOutage(t *testing.T) {
	runWorkerLifecycleOnSource(t, true, false, outageSource(t))
}

// outageSource mounts the source volume for the lifecycle's helper processes.
func outageSource(t *testing.T) func(string) string {
	return func(string) string {
		volume := sourceoutage.Mount(t, sourceoutage.Soft)
		t.Setenv(sourceoutage.EnvControl, volume.Control)
		return filepath.Join(volume.Dir, "source")
	}
}

// Invoked after process recovery and before the ordinary successful promotion.
// The same task and retained checkpoint must remain usable after source failure.
func assertWorkerSourceOutageRetry(t *testing.T, service *MergeService, task *api.WorkerTask, source string) {
	t.Helper()
	if !sourceoutage.Controlled() {
		return
	}
	store, before, published, err := session.OpenTaskCheckpointStore(task)
	testutil.FailErr(t, "open worker checkpoint before source outage", err)
	if !published {
		t.Fatal("recovered worker lacks checkpoint")
	}
	sourceoutage.Stop(t)
	t.Cleanup(func() { sourceoutage.Restore(t) })
	if _, err := os.Stat(filepath.Join(source, "uncached-worker-outage-probe")); err == nil || os.IsNotExist(err) {
		t.Fatalf("source did not report transport failure: %v", err)
	}
	result, err := service.PromoteOverlay(t.Context(), "", task.ID, api.PromoteOverlayInput{})
	if err == nil || result.Status == api.WorkerMergeStatusMerged {
		t.Fatalf("unavailable source accepted worker promotion: result=%+v error=%v", result, err)
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
	sourceoutage.Restore(t)
	assertFileHolds(t, filepath.Join(source, "main.go"), "package main\n")
}
