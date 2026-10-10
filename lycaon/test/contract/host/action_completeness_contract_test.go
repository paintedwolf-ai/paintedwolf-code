package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowPersistenceHasNoRawWorkerCancelUpdate(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, "lycaon", "internal", "workflow", "persistence", "*.go"))
	testutil.FailErr(t, "list workflow persistence files", err)
	if len(paths) == 0 {
		t.Fatal("workflow persistence sources missing")
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, err := os.ReadFile(path)
		testutil.FailErr(t, "read workflow persistence source", err)
		text := string(body)
		if strings.Contains(text, "CancelWorkersByRunID") || strings.Contains(text, "HoldPendingWorkersByRunID") {
			t.Fatalf("%s defines raw worker cancel/hold UPDATE helpers", path)
		}
	}
}

func TestRunStopServiceRoutesCancelThroughSessionAppend(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "worker", "run_stop_service.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read run_stop_service", err)
	text := string(body)
	for _, needle := range []string{"s.Cancellations.Append", "s.Holds.Hold", "CancelActiveByWorkflowRunID"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("run_stop_service.go must route stop through %s", needle)
		}
	}
	bindings := contractcheck.ReadRepoFile(t, root, "lycaon/internal/app/delegations/build.go")
	for _, needle := range []string{"Holds:         deps.Sessions.Manager.Workers.Cards", "Cancellations: deps.Sessions.Manager.Workers.Cancellations"} {
		if !strings.Contains(bindings, needle) {
			t.Fatalf("run stop must bind the actual session outcome owner: %s", needle)
		}
	}
	cancellations := contractcheck.ReadRepoFile(t, root, "lycaon/internal/session/workeroutcomes/cancellations.go")
	cards := contractcheck.ReadRepoFile(t, root, "lycaon/internal/session/workerresults/cards.go")
	holds := contractcheck.ReadRepoFile(t, root, "lycaon/internal/session/workerresults/hold.go")
	if !strings.Contains(cancellations, "m.cards.Project") || !strings.Contains(holds, "m.Project") || !strings.Contains(cards, "m.transcript.Append") || !strings.Contains(cards, "m.transcript.Update") {
		t.Fatal("worker outcomes must reach the authoritative session transcript")
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
