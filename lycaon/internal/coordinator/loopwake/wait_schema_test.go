package loopwake

import (
	"slices"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestWaitSchemaListsEveryAcceptedCondition(t *testing.T) {
	t.Parallel()
	data, err := config.Read(config.ToolSchemasDir.Join("wait.yaml"))
	testutil.FailErr(t, "read wait schema", err)
	var unit struct {
		Schema struct {
			Properties struct {
				Conditions struct {
					Items struct {
						Properties struct {
							Kind struct {
								Enum []string
							}
						}
					}
				}
			}
		}
	}
	testutil.FailErr(t, "decode wait schema", yaml.Unmarshal(data, &unit))
	declared := unit.Schema.Properties.Conditions.Items.Properties.Kind.Enum
	var accepted []string
	for trigger := range validWaitTriggers {
		if trigger != WaitTriggerTimer {
			accepted = append(accepted, string(trigger))
		}
	}
	sort.Strings(accepted)
	sort.Strings(declared)
	if !slices.Equal(declared, accepted) {
		t.Fatalf("wait schema has %v, host accepts %v", declared, accepted)
	}
}
