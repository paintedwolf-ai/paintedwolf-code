package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowSQLStoreHasNoRawWorkerCancelUpdate(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "workflow", "sql_store.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read sql_store", err)
	text := string(body)
	if strings.Contains(text, "CancelWorkersByRunID") || strings.Contains(text, "HoldPendingWorkersByRunID") {
		t.Fatalf("workflow sql_store must not define raw worker cancel/hold UPDATE helpers")
	}
}

func TestRunStopServiceRoutesCancelThroughSessionAppend(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "worker", "run_stop_service.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read run_stop_service", err)
	text := string(body)
	for _, needle := range []string{
		"AppendWorkerCancellation",
		"AppendWorkerHold",
		"CancelActiveByWorkflowRunID",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("run_stop_service.go must route stop through %s", needle)
		}
	}
}

func TestProjectLifecycleParsesWorkerXMLEnvelope(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "search", "project_lifecycle.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read project_lifecycle", err)
	if !strings.Contains(string(body), "workercompletionxml.Unmarshal") {
		t.Fatal("lifecycle projector must parse production XML worker envelopes via workercompletionxml")
	}
}
