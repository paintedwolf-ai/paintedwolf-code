package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// Wait triggers stay aligned across the tool schema, host, and UI.
func TestWaitTriggersAgreeAcrossSchemaHostAndDen(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	schema := waitConditionEnumFromToolSchema(t, root)
	if len(schema) == 0 {
		t.Fatal("no condition kind enum found in tools/schemas/wait.yaml")
	}

	var host []string
	for _, tr := range loopwake.AllWaitTriggers() {
		if tr == loopwake.WaitTriggerTimer {
			continue
		}
		host = append(host, string(tr))
	}
	contractcheck.FailSetEqual(t, "wait condition enum (tools/schemas/wait.yaml vs loopwake consts)", schema, host)

	den := denWaitTriggerLabels(t, root)
	contractcheck.FailSetEqual(t, "wait condition enum (tools/schemas/wait.yaml vs Den label table)", schema, den)
}

func TestEveryWaitRolePublishesReadinessConditions(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	for _, profile := range profiles {
		if !profile.ToolAllowed("wait") {
			continue
		}
		conditions := make(map[string]bool, len(profile.WaitConditions))
		for _, condition := range profile.WaitConditions {
			conditions[condition] = true
		}
		if !conditions["http_ready"] || !conditions["port_ready"] {
			t.Errorf("wait profile %q conditions = %v; every role must support HTTP and port readiness", profile.ID, profile.WaitConditions)
		}
	}
}

func waitConditionEnumFromToolSchema(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf",
		"platform", "tools", "schemas", "wait.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- bundled pack path
	contractcheck.FailErr(t, "read wait.yaml", err)

	var unit struct {
		Schema struct {
			Properties map[string]struct {
				Items struct {
					Properties map[string]struct {
						Enum []string `yaml:"enum"`
					} `yaml:"properties"`
				} `yaml:"items"`
			} `yaml:"properties"`
		} `yaml:"schema"`
	}
	contractcheck.FailErr(t, "decode wait.yaml", yaml.Unmarshal(data, &unit))
	conditions := unit.Schema.Properties["conditions"]
	out := append([]string(nil), conditions.Items.Properties["kind"].Enum...)
	sort.Strings(out)
	return out
}

// denWaitTriggerLabels reads the activity-label trigger keys.
func denWaitTriggerLabels(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "lycaon-den", "src", "chat", "session",
		"thinking-activity-label.ts")
	data, err := os.ReadFile(path) // #nosec G304 -- repo-relative source path
	contractcheck.FailErr(t, "read thinking-activity-label.ts", err)

	block := regexp.MustCompile(`(?s)WAIT_CONDITION_LABELS[^=]*=\s*\{(.*?)\n\};`).FindSubmatch(data)
	if block == nil {
		t.Fatal("WAIT_CONDITION_LABELS not found in thinking-activity-label.ts")
	}
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^\s*([a-z_]+):`).FindAllSubmatch(block[1], -1) {
		if name := string(m[1]); name != "timer" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
