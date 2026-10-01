package contract

import (
	"reflect"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
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
		{"MessageKind", wirespec.GoEnumValues(api.AllMessageKinds()...)},
		{"CompletionReportScope", wirespec.GoEnumValues(api.AllCompletionReportScopes()...)},
		{"DraftStatus", wirespec.GoEnumValues(api.AllDraftStatuses()...)},
		{"MessageVisibility", wirespec.GoEnumValues(api.AllMessageVisibilities()...)},
		{"MessageLiveStatus", wirespec.GoEnumValues(api.AllMessageLiveStatuses()...)},
		{"NavigationEntryKind", wirespec.GoEnumValues(api.AllNavigationEntryKinds()...)},
		{"WorkerStatus", wirespec.GoEnumValues(api.AllWorkerStatuses()...)},
		{"WorkerSummaryStatus", wirespec.GoEnumValues(api.AllWorkerSummaryStatuses()...)},
		{"CheckpointStatus", wirespec.GoEnumValues(api.AllCheckpointStatuses()...)},
		{"WorkflowRunStatus", wirespec.GoEnumValues(api.AllWorkflowRunStatuses()...)},
		{"WorkflowBoundaryKind", wirespec.GoEnumValues(api.AllWorkflowBoundaryKinds()...)},
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
	if len(api.AllMessageKinds()) == 0 {
		t.Fatal("AllMessageKinds empty")
	}
	if len(api.AllDraftStatuses()) == 0 {
		t.Fatal("AllDraftStatuses empty")
	}
	if len(api.AllMessageVisibilities()) == 0 {
		t.Fatal("AllMessageVisibilities empty")
	}
	if len(api.AllMessageLiveStatuses()) == 0 {
		t.Fatal("AllMessageLiveStatuses empty")
	}
	if len(api.AllNavigationEntryKinds()) == 0 {
		t.Fatal("AllNavigationEntryKinds empty")
	}
	if len(api.AllWorkerStatuses()) == 0 {
		t.Fatal("AllWorkerStatuses empty")
	}
	if len(api.AllWorkerSummaryStatuses()) == 0 {
		t.Fatal("AllWorkerSummaryStatuses empty")
	}
	if len(api.AllCheckpointStatuses()) == 0 {
		t.Fatal("AllCheckpointStatuses empty")
	}
	if len(api.AllWorkflowRunStatuses()) == 0 {
		t.Fatal("AllWorkflowRunStatuses empty")
	}
	if len(api.AllWorkflowBoundaryKinds()) == 0 {
		t.Fatal("AllWorkflowBoundaryKinds empty")
	}
	foundPlan := false
	for _, kind := range api.AllMessageKinds() {
		if kind == api.MessageKindBlueprint {
			foundPlan = true
			break
		}
	}
	if !foundPlan {
		t.Fatal("MessageKindBlueprint missing from AllMessageKinds")
	}
}
