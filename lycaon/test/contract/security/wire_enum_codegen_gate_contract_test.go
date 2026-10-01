package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// taskfileDoc is the slice of Taskfile.yml these contracts read.
type taskfileDoc struct {
	PlanGroups map[string][]string `yaml:"-"`
	Tasks      map[string]struct {
		Sources   []string `yaml:"sources"`
		Generates []string `yaml:"generates"`
		Status    []string `yaml:"status"`
		Deps      []depRef `yaml:"deps"`
		Cmds      []cmdRef `yaml:"cmds"`
	} `yaml:"tasks"`
}

type depRef struct {
	Task string `yaml:"task"`
}

// UnmarshalYAML accepts both `- task-name` and `- task: task-name` dep forms.
func (d *depRef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&d.Task)
	}
	type raw depRef
	return node.Decode((*raw)(d))
}

type cmdRef struct {
	Task  string `yaml:"task"`
	Shell string
}

// UnmarshalYAML records shell and structured commands.
func (c *cmdRef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&c.Shell)
	}
	type raw cmdRef
	return node.Decode((*raw)(c))
}

func (c cmdRef) invokedTask() string {
	fields := strings.Fields(c.Shell)
	for index, field := range fields {
		if field == "./task" && index+1 < len(fields) {
			return fields[index+1]
		}
	}
	return ""
}

func loadTaskfile(t *testing.T) taskfileDoc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "Taskfile.yml"))
	contractcheck.FailErr(t, "read Taskfile.yml", err)
	var doc taskfileDoc
	contractcheck.FailErr(t, "parse Taskfile.yml", yaml.Unmarshal(data, &doc))
	if len(doc.Tasks) == 0 {
		t.Fatal("Taskfile.yml parsed with no tasks")
	}
	planData, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "scripts", "verification-plan.json"))
	contractcheck.FailErr(t, "read verification plan", err)
	var plan struct {
		Groups map[string][]string `json:"groups"`
	}
	contractcheck.FailErr(t, "parse verification plan", json.Unmarshal(planData, &plan))
	doc.PlanGroups = plan.Groups
	return doc
}

// Enumerated fingerprints can miss newly added generated files.
func TestWireEnumCodegenTasksDeclareNoFingerprint(t *testing.T) {
	t.Parallel()
	doc := loadTaskfile(t)

	for _, name := range []string{"codegen:wire-enums", "codegen:wire-enums:check"} {
		task, ok := doc.Tasks[name]
		if !ok {
			t.Errorf("Taskfile.yml no longer defines %s", name)
			continue
		}
		if len(task.Sources) > 0 {
			t.Errorf("%s declares sources %v; Task would skip it over generated files "+
				"the list does not name", name, task.Sources)
		}
		if len(task.Generates) > 0 {
			t.Errorf("%s declares generates %v; the output set is per-enum", name, task.Generates)
		}
		if len(task.Status) > 0 {
			t.Errorf("%s declares status %v; same skip hazard as sources", name, task.Status)
		}
	}
}

func (c cmdRef) invokedPlanGroup() string {
	fields := strings.Fields(c.Shell)
	if len(fields) == 5 && fields[0] == "python3" &&
		filepath.Base(fields[1]) == "test-execution.py" && fields[2] == "plan" && fields[3] == "--" {
		return fields[4]
	}
	return ""
}

// reaches follows task calls and declared verification groups.
func (d taskfileDoc) reaches(from, want string) bool {
	seen := map[string]bool{}
	var walk, group func(string) bool
	group = func(name string) bool {
		if name == want {
			return true
		}
		key := "plan:" + name
		if seen[key] {
			return false
		}
		seen[key] = true
		for _, member := range d.PlanGroups[name] {
			if _, nested := d.PlanGroups[member]; nested {
				if group(member) {
					return true
				}
			} else if walk(member) {
				return true
			}
		}
		return false
	}
	walk = func(name string) bool {
		if name == want {
			return true
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		task, ok := d.Tasks[name]
		if !ok {
			return false
		}
		for _, dep := range task.Deps {
			if dep.Task != "" && walk(dep.Task) {
				return true
			}
		}
		for _, cmd := range task.Cmds {
			if invoked := cmd.invokedPlanGroup(); invoked != "" && group(invoked) {
				return true
			}
			if cmd.Task != "" && walk(cmd.Task) {
				return true
			}
			if invoked := cmd.invokedTask(); invoked != "" && walk(invoked) {
				return true
			}
		}
		return false
	}
	return walk(from)
}

func TestWireEnumCheckIsWiredIntoTheGate(t *testing.T) {
	t.Parallel()
	doc := loadTaskfile(t)

	if _, ok := doc.Tasks["check"]; !ok {
		t.Fatal("Taskfile.yml no longer defines the check gate")
	}
	if !doc.reaches("check", "codegen:wire-enums:check") {
		t.Error("check does not reach codegen:wire-enums:check — the wire-enum " +
			"vocabulary would drift from its generated outputs unnoticed")
	}
}
