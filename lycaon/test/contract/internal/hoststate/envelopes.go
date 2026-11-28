package hoststate

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

// AnchorVarSpec describes one variable declared under an anchor's envelope.vars.
type AnchorVarSpec struct {
	Name        string `yaml:"-"`
	Type        string `yaml:"type"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
}

// AnchorEnvelopeSpec describes an anchor's declared surface, planes, and envelope vars.
type AnchorEnvelopeSpec struct {
	ID           string
	TriggerClass string
	Surface      string
	Planes       []string
	Vars         []AnchorVarSpec
}

type anchorCatalogDocument struct {
	Anchors []struct {
		ID           string   `yaml:"id"`
		TriggerClass string   `yaml:"trigger_class"`
		Surface      string   `yaml:"surface"`
		Planes       []string `yaml:"planes"`
		Envelope     struct {
			Vars map[string]AnchorVarSpec `yaml:"vars"`
		} `yaml:"envelope"`
	} `yaml:"anchors"`
}

var loadAnchorEnvelopeSpecs = sync.OnceValues(func() (map[string]AnchorEnvelopeSpec, error) {
	raw, err := config.Read(config.AnchorCatalog)
	if err != nil {
		return nil, fmt.Errorf("read anchor catalog: %w", err)
	}
	var doc anchorCatalogDocument
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode anchor catalog: %w", err)
	}
	if len(doc.Anchors) == 0 {
		return nil, fmt.Errorf("anchor catalog contains no anchors")
	}
	out := make(map[string]AnchorEnvelopeSpec, len(doc.Anchors))
	for _, row := range doc.Anchors {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			continue
		}
		names := make([]string, 0, len(row.Envelope.Vars))
		for name := range row.Envelope.Vars {
			names = append(names, name)
		}
		slices.Sort(names)
		vars := make([]AnchorVarSpec, 0, len(names))
		for _, name := range names {
			spec := row.Envelope.Vars[name]
			spec.Name = name
			spec.Type = strings.TrimSpace(spec.Type)
			vars = append(vars, spec)
		}
		out[id] = AnchorEnvelopeSpec{
			ID:           id,
			TriggerClass: strings.TrimSpace(row.TriggerClass),
			Surface:      strings.TrimSpace(row.Surface),
			Planes:       slices.Clone(row.Planes),
			Vars:         vars,
		}
	}
	return out, nil
})

// AnchorCatalogSpecs returns all parsed anchor envelope specifications keyed by anchor ID.
func AnchorCatalogSpecs() (map[string]AnchorEnvelopeSpec, error) {
	specs, err := loadAnchorEnvelopeSpecs()
	if err != nil {
		return nil, err
	}
	return maps.Clone(specs), nil
}

// AnchorEnvelopes emits deterministic boundary variable maps for anchorID from
// config/packs/painted-wolf/platform/host/anchors/catalog.yaml.
func AnchorEnvelopes(anchorID string) ([]map[string]any, error) {
	specs, err := loadAnchorEnvelopeSpecs()
	if err != nil {
		return nil, err
	}
	spec, ok := specs[strings.TrimSpace(anchorID)]
	if !ok {
		return nil, fmt.Errorf("unknown anchor %q in catalog", anchorID)
	}
	if len(spec.Vars) == 0 {
		return []map[string]any{{}}, nil
	}

	domains := make([][]any, len(spec.Vars))
	productSize := 1
	const maxFullCartesian = 32
	for i, v := range spec.Vars {
		vals, domErr := boundaryValuesForVar(v)
		if domErr != nil {
			return nil, fmt.Errorf("anchor %s var %s: %w", anchorID, v.Name, domErr)
		}
		domains[i] = vals
		if productSize <= maxFullCartesian {
			productSize *= len(vals)
		}
	}

	if productSize <= maxFullCartesian {
		return expandEnvelopeProduct(spec.Vars, domains), nil
	}
	return expandBoundedEnvelopeSweep(spec.Vars, domains), nil
}

func expandEnvelopeProduct(vars []AnchorVarSpec, domains [][]any) []map[string]any {
	out := []map[string]any{{}}
	for i, v := range vars {
		next := make([]map[string]any, 0, len(out)*len(domains[i]))
		for _, prefix := range out {
			for _, candidate := range domains[i] {
				row := maps.Clone(prefix)
				if candidate != nil || v.Required {
					row[v.Name] = candidate
				}
				next = append(next, row)
			}
		}
		out = next
	}
	return dedupeEnvelopeMaps(out)
}

func expandBoundedEnvelopeSweep(vars []AnchorVarSpec, domains [][]any) []map[string]any {
	buildBaseline := func(pick func([]any) any) map[string]any {
		base := make(map[string]any, len(vars))
		for i, v := range vars {
			val := pick(domains[i])
			if val != nil || v.Required {
				base[v.Name] = val
			}
		}
		return base
	}
	minimal := buildBaseline(func(vals []any) any { return vals[0] })
	singular := buildBaseline(func(vals []any) any { return vals[min(1, len(vals)-1)] })
	plural := buildBaseline(func(vals []any) any { return vals[len(vals)-1] })

	candidates := []map[string]any{minimal, singular, plural}
	for _, base := range []map[string]any{minimal, plural} {
		for i, v := range vars {
			for _, val := range domains[i] {
				variant := maps.Clone(base)
				if val == nil && !v.Required {
					delete(variant, v.Name)
				} else {
					variant[v.Name] = val
				}
				candidates = append(candidates, variant)
			}
		}
	}
	return dedupeEnvelopeMaps(candidates)
}

func dedupeEnvelopeMaps(in []map[string]any) []map[string]any {
	seen := make(map[string]bool, len(in))
	out := make([]map[string]any, 0, len(in))
	for _, m := range in {
		enc, _ := encodeAdmittedValue(reflect.ValueOf(m), map[ptrVisit]bool{})
		digest := sha256CanonicalJSON(enc)
		if seen[digest] {
			continue
		}
		seen[digest] = true
		out = append(out, m)
	}
	return out
}

// AnchorVarDomain returns the typed values an envelope variable takes in the
// envelope sweep: both truths, zero, one and a plural count, empty and sample
// strings, and empty, one- and two-item collections.
func AnchorVarDomain(v AnchorVarSpec) ([]any, error) { return boundaryValuesForVar(v) }

func boundaryValuesForVar(v AnchorVarSpec) ([]any, error) {
	switch v.Type {
	case "bool":
		return []any{false, true}, nil
	case "int":
		return []any{0, 1, pluralThresholdForVar(v.Name)}, nil
	case "string":
		sample := sampleStringForVar(v.Name)
		if v.Required {
			return []any{"", sample}, nil
		}
		return []any{"", sample}, nil
	case "string[]":
		first, second := sampleListItemsForVar(v.Name)
		return []any{[]string{}, []string{first}, []string{first, second}}, nil
	case "object":
		return []any{nil, sampleObjectForVar(v.Name, 0)}, nil
	case "object[]":
		one := sampleObjectForVar(v.Name, 0)
		two := sampleObjectForVar(v.Name, 1)
		return []any{[]map[string]any{}, []map[string]any{one}, []map[string]any{one, two}}, nil
	default:
		return nil, fmt.Errorf("unsupported envelope var type %q", v.Type)
	}
}

func pluralThresholdForVar(name string) int {
	switch name {
	case "tokens":
		return 12000
	case "model_calls", "tool_calls", "tools_done":
		return 4
	case "quiet_minutes", "tools_minutes":
		return 5
	default:
		return 2
	}
}

func sampleStringForVar(name string) string {
	switch name {
	case "tool", "held_call_tool", "trigger_tool":
		return "command"
	case "profile":
		return "coordinator"
	case "session_posture":
		return "build"
	case "notice_kind":
		return "consumption"
	case "agent_type":
		return "implementer"
	case "producer_state":
		return "rejected"
	case "recipient":
		return "coordinator"
	case "view":
		return "observations"
	case "declared_command":
		return "ls -la"
	case "skill_name":
		return "verify-before-reporting"
	case "skill_body":
		return "Check observed results before reporting."
	case "workflow", "workflow_id":
		return "implement"
	case "phase", "phase_id", "run_phase", "batch_phase":
		return "work"
	case "scope_mode":
		return "write"
	default:
		return "sample-" + strings.ReplaceAll(name, "_", "-")
	}
}

func sampleListItemsForVar(name string) (string, string) {
	switch name {
	case "tools", "offered_tools":
		return "recall", "command"
	case "promoted_paths", "diffs":
		return "internal/app/a.go", "internal/app/b.go"
	case "failed_leaves":
		return "fanout_planned", "worker_cycle_ready"
	default:
		clean := strings.ReplaceAll(name, "_", "-")
		return clean + "-1", clean + "-2"
	}
}

func sampleObjectForVar(name string, index int) map[string]any {
	suffix := fmt.Sprintf("%d", index+1)
	switch name {
	case "editors":
		return map[string]any{
			"path": "src/file" + suffix + ".go", "unsaved": index == 0,
			"selected": index == 0, "start_line": 10, "end_line": 20, "cursor": false,
		}
	case "findings":
		return map[string]any{
			"rule_id": "RULE_" + suffix, "level": "warning", "location": "src/file" + suffix + ".go:12",
		}
	case "files":
		return map[string]any{
			"path": "src/file" + suffix + ".go", "actor": "user", "detail": "edited",
			"op": "modify", "at": "1m ago", "effects": 1,
			"working_set": index == 0, "during_your_command": false, "handle": "source_change#" + suffix,
		}
	case "git":
		return map[string]any{"op": "commit", "detail": "HEAD -> abc" + suffix, "at": "1m ago"}
	case "jobs":
		return map[string]any{
			"handle": "cmd-" + suffix, "kind": "command", "state": "running", "command": "ls",
		}
	case "roots":
		return map[string]any{"id": "r" + suffix, "label": "root-" + suffix, "path": "/tmp/root-" + suffix, "is_primary": index == 0}
	case "after":
		return map[string]any{"label": "root-" + suffix, "is_active": index == 0}
	case "changes":
		kinds := []string{"added", "removed", "renamed"}
		return map[string]any{"kind": kinds[index%len(kinds)], "label": "root-" + suffix, "old_label": "prev-" + suffix}
	case "session_folder_changed", "new_primary", "active_root":
		return map[string]any{"label": "root-" + suffix}
	case "gate_obligations":
		ids := []string{"fanout_planned", "worker_cycle_ready"}
		return map[string]any{"id": ids[index%len(ids)], "description": "Complete obligation " + suffix}
	default:
		return map[string]any{"id": name + "-" + suffix, "summary": "item " + suffix, "status": "ok"}
	}
}
