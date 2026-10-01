package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/toolschema"
)

const configPath = "config/packs/painted-wolf/platform/tools/tool-presentation.yaml"

const schemaDir = "config/packs/painted-wolf/platform/tools/schemas"

// Visibility is the static transcript visibility class.
type Visibility string

const (
	VisibilityDefault             Visibility = "default"
	VisibilityCoordinatorInternal Visibility = "coordinator_internal"
	VisibilityLongRunning         Visibility = "long_running"
)

var validVisibility = map[Visibility]bool{
	VisibilityDefault:             true,
	VisibilityCoordinatorInternal: true,
	VisibilityLongRunning:         true,
}

// Kind selects the tool detail renderer.
type Kind string

const (
	KindGeneric Kind = "generic"
	KindRead    Kind = "read"
	KindWrite   Kind = "write"
	KindCommand Kind = "command"
	KindTask    Kind = "task"
	KindSkill   Kind = "skill"
)

var validKind = map[Kind]bool{
	KindGeneric: true,
	KindRead:    true,
	KindWrite:   true,
	KindCommand: true,
	KindTask:    true,
	KindSkill:   true,
}

// PathEntry names what a tool's `path` argument addresses.
type PathEntry string

const (
	PathEntryFile   PathEntry = "file"
	PathEntryFolder PathEntry = "folder"
)

var validPathEntry = map[PathEntry]bool{
	PathEntryFile:   true,
	PathEntryFolder: true,
}

// ActivitySalience weights a tool's headline vote.
type ActivitySalience string

const (
	ActivityIncidental ActivitySalience = "incidental"
	ActivitySupporting ActivitySalience = "supporting"
	ActivityPrimary    ActivitySalience = "primary"
	ActivityDefining   ActivitySalience = "defining"
)

var activitySalienceWeights = map[ActivitySalience]int{
	ActivityIncidental: 0,
	ActivitySupporting: 1,
	ActivityPrimary:    3,
	ActivityDefining:   6,
}

// ActivityRole categorizes a tool's semantic function in workflows.
type ActivityRole string

const (
	RoleInterfaceTest ActivityRole = "interface_test"
	RoleEndpoint      ActivityRole = "endpoint"
	RoleAutomatedTest ActivityRole = "automated_test"
	RoleMutation      ActivityRole = "mutation"
	RoleInvestigation ActivityRole = "investigation"
	RoleCommand       ActivityRole = "command"
	RoleInspection    ActivityRole = "inspection"
	RoleResearch      ActivityRole = "research"
	RoleCoordination  ActivityRole = "coordination"
	RoleProcess       ActivityRole = "process"
	RoleSecurityScan  ActivityRole = "security_scan"
)

// activityRoleTiers ranks roles by intent specificity; higher tiers never yield to lower tiers.
var activityRoleTiers = map[ActivityRole]int{
	RoleInvestigation: 1,
	RoleCommand:       1,
	RoleSecurityScan:  1,
	RoleEndpoint:      2,
	RoleInspection:    2,
	RoleProcess:       2,
	RoleCoordination:  2,
	RoleResearch:      3,
	RoleAutomatedTest: 3,
	RoleMutation:      3,
	RoleInterfaceTest: 3,
}

// ActivityRoles lists the roles in tier order, then by name.
func ActivityRoles() []ActivityRole {
	roles := make([]ActivityRole, 0, len(activityRoleTiers))
	for role := range activityRoleTiers {
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool {
		if activityRoleTiers[roles[i]] != activityRoleTiers[roles[j]] {
			return activityRoleTiers[roles[i]] < activityRoleTiers[roles[j]]
		}
		return roles[i] < roles[j]
	})
	return roles
}

// Tier is the role's headline rank.
func (r ActivityRole) Tier() int {
	return activityRoleTiers[r]
}

// ActivityPresentation is a tool's headline vote.
type ActivityPresentation struct {
	Headline string           `yaml:"headline"`
	Salience ActivitySalience `yaml:"salience"`
	Role     ActivityRole     `yaml:"role"`
}

// Entry is one tool's transcript presentation metadata.
type Entry struct {
	Kind          Kind                 `yaml:"kind"`
	TitleKeys     []string             `yaml:"title_keys"`
	TitleFallback string               `yaml:"title_fallback"`
	RunningLabel  string               `yaml:"running_label"`
	Visibility    Visibility           `yaml:"visibility"`
	PathEntry     PathEntry            `yaml:"path_entry"`
	Activity      ActivityPresentation `yaml:"activity"`
}

// Config is the parsed tool-presentation.yaml.
type Config struct {
	FallbackTitleKeys []string         `yaml:"fallback_title_keys"`
	Tools             map[string]Entry `yaml:"tools"`
}

// Load reads and parses the presentation catalog at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- bundled pack path from the Taskfile
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(cfg.Tools) == 0 {
		return Config{}, fmt.Errorf("%s: no tools declared", path)
	}
	return cfg, nil
}

// Validate rejects catalog and schema drift.
func (c Config) Validate(schemas *toolschema.Config) error {
	var problems []string

	if len(c.FallbackTitleKeys) == 0 {
		problems = append(problems, "fallback_title_keys is empty")
	}
	seenFallback := map[string]bool{}
	for _, key := range c.FallbackTitleKeys {
		if seenFallback[key] {
			problems = append(problems, fmt.Sprintf("fallback_title_keys: duplicate %q", key))
		}
		seenFallback[key] = true
	}

	for _, name := range sortedKeys(schemas.Tools) {
		if _, ok := c.Tools[name]; !ok {
			problems = append(problems, fmt.Sprintf(
				"tool %q has tools/schemas/%s.yaml but no tool-presentation.yaml entry", name, name))
		}
	}

	for _, name := range sortedKeys(c.Tools) {
		entry := c.Tools[name]
		schema, ok := schemas.Tools[name]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"tool %q has a tool-presentation.yaml entry but no tools/schemas/%s.yaml", name, name))
			continue
		}

		vis := entry.Visibility
		if vis == "" {
			vis = VisibilityDefault
		}
		if !validVisibility[vis] {
			problems = append(problems, fmt.Sprintf("tool %q: unknown visibility %q", name, vis))
		}
		kind := entry.Kind
		if kind == "" {
			kind = KindGeneric
		}
		if !validKind[kind] {
			problems = append(problems, fmt.Sprintf("tool %q: unknown kind %q", name, kind))
		}
		if kind == KindTask && len(entry.TitleKeys) > 0 {
			problems = append(problems, fmt.Sprintf(
				"tool %q: kind task routes through TaskCard, so title_keys is dead data — remove it", name))
		}
		// Task visibility follows its settled result.
		if kind == KindTask && vis == VisibilityLongRunning {
			problems = append(problems, fmt.Sprintf(
				"tool %q: kind task settles on ui_visibility, so it cannot be long_running — a refused dispatch would render and then vanish",
				name))
		}
		if entry.RunningLabel != "" && strings.TrimSpace(entry.RunningLabel) == "" {
			problems = append(problems, fmt.Sprintf("tool %q: blank running_label", name))
		}
		if entry.Activity.Headline == "" {
			problems = append(problems, fmt.Sprintf("tool %q: activity.headline is required", name))
		} else if entry.Activity.Headline != strings.TrimSpace(entry.Activity.Headline) {
			problems = append(problems, fmt.Sprintf(
				"tool %q: activity.headline %q has surrounding whitespace", name, entry.Activity.Headline))
		}
		if _, ok := activitySalienceWeights[entry.Activity.Salience]; !ok {
			problems = append(problems, fmt.Sprintf(
				"tool %q: unknown activity.salience %q", name, entry.Activity.Salience))
		}
		if _, known := activityRoleTiers[entry.Activity.Role]; entry.Activity.Role != "" && !known {
			problems = append(problems, fmt.Sprintf(
				"tool %q: unknown activity.role %q", name, entry.Activity.Role))
		}

		args := argNames(schema)
		if entry.PathEntry != "" {
			if !validPathEntry[entry.PathEntry] {
				problems = append(problems, fmt.Sprintf("tool %q: unknown path_entry %q", name, entry.PathEntry))
			} else if _, ok := titleKeyProperty(schema, "path"); !ok {
				problems = append(problems, fmt.Sprintf(
					"tool %q: path_entry is declared but tools/schemas/%s.yaml has no path property", name, name))
			}
		}
		for _, key := range entry.TitleKeys {
			if !args[key] {
				problems = append(problems, fmt.Sprintf(
					"tool %q: title key %q is not a property or arg alias of tools/schemas/%s.yaml", name, key, name))
				continue
			}
			prop, ok := titleKeyProperty(schema, key)
			if !ok {
				continue
			}
			if values := closedEnum(prop); len(values) > 0 {
				problems = append(problems, fmt.Sprintf(
					"tool %q: title key %q is a closed enum (%s) — the subtitle would print a fixed token, not content; drop the key or name a free-text property",
					name, key, strings.Join(values, "|")))
				continue
			}
			if described, printable := displayableTitleKey(prop); !printable {
				problems = append(problems, fmt.Sprintf(
					"tool %q: title key %q is %s — Den prints a string, an array of strings, or an array of {from,to}; this key always resolves to no subtitle. Name a string property or set title_keys: []",
					name, key, described))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("tool-presentation.yaml drift:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// ActivityPresentations returns every catalog headline vote.
func (c Config) ActivityPresentations() map[string]ActivityPresentation {
	out := make(map[string]ActivityPresentation, len(c.Tools))
	for name, entry := range c.Tools {
		out[name] = entry.Activity
	}
	return out
}

func (a ActivityPresentation) Weight() int {
	return activitySalienceWeights[a.Salience]
}

// argNames returns the schema property names a tool accepts.
func argNames(entry toolschema.Entry) map[string]bool {
	out := map[string]bool{}
	required, optional := toolschema.ArgFieldSummary(entry.Schema)
	for _, name := range required {
		out[name] = true
	}
	for _, name := range optional {
		out[name] = true
	}
	return out
}

// titleKeyProperty resolves a title key to its schema property.
func titleKeyProperty(entry toolschema.Entry, key string) (map[string]any, bool) {
	props, ok := entry.Schema["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	prop, ok := props[key].(map[string]any)
	return prop, ok
}

// closedEnum returns fixed string values.
func closedEnum(prop map[string]any) []string {
	raw, ok := prop["enum"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// displayableTitleKey matches the runtime subtitle shapes.
func displayableTitleKey(prop map[string]any) (described string, printable bool) {
	switch propType(prop) {
	case "string":
		return "a string", true
	case "array":
		items, ok := prop["items"].(map[string]any)
		if !ok {
			return "an array with no declared item type", false
		}
		switch propType(items) {
		case "string":
			return "an array of strings", true
		case "object":
			itemProps, _ := items["properties"].(map[string]any)
			_, hasFrom := itemProps["from"]
			_, hasTo := itemProps["to"]
			if hasFrom && hasTo {
				return "an array of {from,to}", true
			}
			return "an array of objects without from/to", false
		default:
			return "an array of " + propType(items), false
		}
	case "":
		return "a property with no declared type", false
	default:
		return "a" + article(propType(prop)) + propType(prop), false
	}
}

func propType(prop map[string]any) string {
	t, _ := prop["type"].(string)
	return t
}

func article(t string) string {
	if strings.HasPrefix(t, "i") || strings.HasPrefix(t, "o") {
		return "n "
	}
	return " "
}

// ToolsWithVisibility lists tools in the given class, sorted.
func (c Config) ToolsWithVisibility(want Visibility) []string {
	var out []string
	for name, entry := range c.Tools {
		vis := entry.Visibility
		if vis == "" {
			vis = VisibilityDefault
		}
		if vis == want {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ToolsWithKind lists tools in the given rendering family, sorted.
func (c Config) ToolsWithKind(want Kind) []string {
	var out []string
	for name, entry := range c.Tools {
		kind := entry.Kind
		if kind == "" {
			kind = KindGeneric
		}
		if kind == want {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TitleKeys returns ordered per-tool title keys.
func (c Config) TitleKeys() map[string][]string {
	out := map[string][]string{}
	for name, entry := range c.Tools {
		out[name] = append([]string{}, entry.TitleKeys...)
	}
	return out
}

// Kinds is tool → card rendering family for tools that are not generic.
func (c Config) Kinds() map[string]Kind {
	out := map[string]Kind{}
	for name, entry := range c.Tools {
		if entry.Kind != "" && entry.Kind != KindGeneric {
			out[name] = entry.Kind
		}
	}
	return out
}

// PathEntries is tool → the entry kind its path argument addresses.
func (c Config) PathEntries() map[string]PathEntry {
	out := map[string]PathEntry{}
	for name, entry := range c.Tools {
		if entry.PathEntry != "" {
			out[name] = entry.PathEntry
		}
	}
	return out
}

// RunningLabels is tool → in-flight label for tools that declare one.
func (c Config) RunningLabels() map[string]string {
	out := map[string]string{}
	for name, entry := range c.Tools {
		if entry.RunningLabel != "" {
			out[name] = entry.RunningLabel
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func schemaDirPath(root string) string { return filepath.Join(root, schemaDir) }

func configFilePath(root string) string { return filepath.Join(root, configPath) }
