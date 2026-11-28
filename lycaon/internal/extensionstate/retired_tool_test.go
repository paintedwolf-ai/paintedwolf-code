package extensionstate_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// recordedRetirement reads a tool's successor from the shipped retirement catalog.
func recordedRetirement(t *testing.T, name string) toolcontract.Retirement {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "..", "schemas", "agent-public-retirements.json"))
	testutil.FailErr(t, "read retirement catalog", err)
	var doc struct {
		Vocabularies struct {
			Tools map[string]json.RawMessage `json:"tools"`
		} `json:"vocabularies"`
	}
	testutil.FailErr(t, "decode retirement catalog", json.Unmarshal(raw, &doc))
	entry, ok := doc.Vocabularies.Tools[name]
	if !ok {
		t.Fatalf("%s is not recorded as retired", name)
	}
	var out toolcontract.Retirement
	if json.Unmarshal(entry, &out.Successors) == nil {
		return out
	}
	var contract struct {
		ContentContract string `json:"content_contract"`
	}
	testutil.FailErr(t, "decode content contract", json.Unmarshal(entry, &contract))
	out.ContentContract = contract.ContentContract
	return out
}

// A pack naming a retired tool is refused with the recorded successor, and the
// refusal leaves extension state as it was.
func TestPackNamingRetiredToolIsRefusedWithItsSuccessor(t *testing.T) {
	for _, name := range []string{
		"handoff_health", "handoff_init", "parse_evaluation", "parse_extract_json", "parse_validate",
		"plan_append_review_evidence", "state_close", "state_query", "state_update", "workflow_user_feedback",
	} {
		t.Run(name, func(t *testing.T) {
			owner := testOwner(t)
			author := writeAuthorPack(t, "acme/retired", map[string]string{
				"tools/profiles/acme_retired.yaml": "id: acme_retired\ndescription: Names a retired tool\n" +
					"command_effects: [read_only]\ntools:\n  " + name + ": true\n",
			})
			before := currentRevision(t, owner, "")
			_, err := owner.Apply(t.Context(), extensionstate.Intent{
				Scope:            extensionstate.Scope{Kind: "device"},
				ExpectedRevision: before,
				Op:               extensionstate.InstallOp{Source: "path:" + author},
			})
			var rejected *extensionstate.RejectedError
			var retired *toolcontract.RetiredToolError
			if !errors.As(err, &rejected) || !errors.As(err, &retired) {
				t.Fatalf("install naming %s = %v, want a retired-tool rejection", name, err)
			}
			want := recordedRetirement(t, name)
			if retired.Tool != name || retired.Retirement.ContentContract != want.ContentContract ||
				!slices.Equal(retired.Retirement.Successors, want.Successors) {
				t.Fatalf("refusal = %+v, want %s with %+v", retired, name, want)
			}
			if got := currentRevision(t, owner, ""); got != before {
				t.Fatalf("refusal moved the revision: %s vs %s", got, before)
			}
			path, err := extpacks.DeviceDesiredPath()
			testutil.FailErr(t, "device desired path", err)
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				desired, loadErr := extpacks.LoadDesiredFile(path)
				testutil.FailErr(t, "load desired", loadErr)
				if _, ok := extpacks.DesiredPackRow(desired, "acme/retired"); ok {
					t.Fatal("refused pack reached desired state")
				}
			}
		})
	}
}
