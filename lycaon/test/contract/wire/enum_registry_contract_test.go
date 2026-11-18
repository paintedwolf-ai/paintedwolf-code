package contract

import (
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/pkg/api/apitest"
	"reflect"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// TestAllEnumRegistriesExhaustive requires every string const declared for a
// tracked enum type in pkg/api to appear in its All…() registry.
func TestAllEnumRegistriesExhaustive(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	discovered, err := wirespec.DiscoverAPIStringEnums(root)
	testutil.FailErr(t, "discoverAPIStringEnums", err)

	checks := []struct {
		typeName string
		registry []string
	}{
		{"MessageKind", wirespec.GoEnumValues(apitest.AllMessageKinds()...)},
		{"CompletionReportScope", wirespec.GoEnumValues(apitest.AllCompletionReportScopes()...)},
		{"DraftStatus", wirespec.GoEnumValues(apitest.AllDraftStatuses()...)},
		{"MessageVisibility", wirespec.GoEnumValues(apitest.AllMessageVisibilities()...)},
		{"MessageLiveStatus", wirespec.GoEnumValues(apitest.AllMessageLiveStatuses()...)},
		{"NavigationEntryKind", wirespec.GoEnumValues(apitest.AllNavigationEntryKinds()...)},
		{"WorkerStatus", wirespec.GoEnumValues(apitest.AllWorkerStatuses()...)},
		{"WorkerSummaryStatus", wirespec.GoEnumValues(apitest.AllWorkerSummaryStatuses()...)},
		{"CheckpointStatus", wirespec.GoEnumValues(apitest.AllCheckpointStatuses()...)},
		{"WorkflowRunStatus", wirespec.GoEnumValues(api.AllWorkflowRunStatuses()...)},
		{"WorkflowBoundaryKind", wirespec.GoEnumValues(apitest.AllWorkflowBoundaryKinds()...)},
	}
	for _, check := range checks {
		t.Run(check.typeName, func(t *testing.T) {
			t.Parallel()
			want := discovered[check.typeName]
			if len(want) == 0 {
				t.Fatalf("no consts discovered for %s", check.typeName)
			}
			got := append([]string(nil), check.registry...)
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s All…() registry mismatch\n  registry: %v\n  consts:   %v", check.typeName, got, want)
			}
		})
	}
}

// Enum registry smoke — keeps All…() entry points reachable for property tests.
func TestEnumRegistriesNonEmpty(t *testing.T) {
	t.Parallel()
	if len(apitest.AllMessageKinds()) == 0 {
		t.Fatal("AllMessageKinds empty")
	}
	if len(apitest.AllDraftStatuses()) == 0 {
		t.Fatal("AllDraftStatuses empty")
	}
	if len(apitest.AllMessageVisibilities()) == 0 {
		t.Fatal("AllMessageVisibilities empty")
	}
	if len(apitest.AllMessageLiveStatuses()) == 0 {
		t.Fatal("AllMessageLiveStatuses empty")
	}
	if len(apitest.AllNavigationEntryKinds()) == 0 {
		t.Fatal("AllNavigationEntryKinds empty")
	}
	if len(apitest.AllWorkerStatuses()) == 0 {
		t.Fatal("AllWorkerStatuses empty")
	}
	if len(apitest.AllWorkerSummaryStatuses()) == 0 {
		t.Fatal("AllWorkerSummaryStatuses empty")
	}
	if len(apitest.AllCheckpointStatuses()) == 0 {
		t.Fatal("AllCheckpointStatuses empty")
	}
	if len(api.AllWorkflowRunStatuses()) == 0 {
		t.Fatal("AllWorkflowRunStatuses empty")
	}
	if len(apitest.AllWorkflowBoundaryKinds()) == 0 {
		t.Fatal("AllWorkflowBoundaryKinds empty")
	}
	foundPlan := false
	for _, kind := range apitest.AllMessageKinds() {
		if kind == api.MessageKindBlueprint {
			foundPlan = true
			break
		}
	}
	if !foundPlan {
		t.Fatal("MessageKindBlueprint missing from AllMessageKinds")
	}
}
