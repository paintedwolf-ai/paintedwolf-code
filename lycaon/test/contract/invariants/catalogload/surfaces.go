package catalogload

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// skillFrontmatterKeys are the documented top-level SKILL.md keys.
var skillFrontmatterKeys = []string{"name", "description", "license", "compatibility", "metadata", "allowed-tools"}

var surfaces = []invariants.Invariant{{
	// Every selectable surface has a short, capitalised, distinct activity
	// label without its own trailing marker; Den adds the marker.
	Name:    "coordinator-surfaces-declare-activity-labels",
	Anchors: []invariants.Anchor{invariants.Func("internal/coordinator/surface", "LoadCoordinatorSurfaceActivityLabel")},
	Check: func(r *invariants.Run) {
		plans, err := surface.CompileToolPlans(1)
		r.Must(err)
		r.Expect(len(plans), "coordinator surfaces")
		seen := map[string]string{}
		for id := range plans {
			label := surface.LoadCoordinatorSurfaceActivityLabel(id)
			first, _ := utf8.DecodeRuneInString(label)
			switch {
			case strings.TrimSpace(label) == "":
				r.Report("surface %s has no activity_label", id)
				continue
			case label != strings.TrimSpace(label):
				r.Report("surface %s activity_label %q has surrounding whitespace", id, label)
			case strings.HasSuffix(label, ".") || strings.HasSuffix(label, "…"):
				r.Report("surface %s activity_label %q ends in its own punctuation", id, label)
			case utf8.RuneCountInString(label) > 48:
				r.Report("surface %s activity_label exceeds the 48 runes the composer lane shows", id)
			case !unicode.IsUpper(first):
				r.Report("surface %s activity_label %q does not start with a capital", id, label)
			}
			if prev, dup := seen[label]; dup {
				r.Report("surfaces %s and %s share activity_label %q", prev, id, label)
			}
			seen[label] = id
		}
	},
}, {
	// A surface and the batch-phase chip for the same phase share one label.
	Name:    "batch-phase-labels-twin-their-surfaces",
	Anchors: []invariants.Anchor{invariants.File("docs/openapi/vocab/CoordinatorBatchPhase.yaml")},
	Check: func(r *invariants.Run) {
		var doc struct {
			Values []struct {
				ID    string `yaml:"id"`
				Label string `yaml:"label"`
			} `yaml:"values"`
		}
		r.Must(yaml.Unmarshal(r.Bytes("docs/openapi/vocab/CoordinatorBatchPhase.yaml"), &doc))
		labels := map[string]string{}
		for _, v := range doc.Values {
			labels[v.ID] = v.Label
		}
		for surfaceID, phase := range map[string]string{"implement_dispatch": "dispatch", "implement_overlay_promote": "integrate", "implement_synthesis": "synthesize"} {
			if got := surface.LoadCoordinatorSurfaceActivityLabel(surfaceID); labels[phase] == "" || got != labels[phase] {
				r.Report("surface %s activity_label %q differs from batch phase %s label %q", surfaceID, got, phase, labels[phase])
			}
		}
	},
}, {
	// Every implement surface maps to an execution-mode family.
	Name:    "implement-surfaces-map-to-an-execution-mode-family",
	Anchors: []invariants.Anchor{invariants.File("lycaon/config/packs/painted-wolf/platform/host/coordinator-surfaces.yaml")},
	Check: func(r *invariants.Run) {
		var all map[string]any
		r.Must(yaml.Unmarshal(r.Bytes("lycaon/config/packs/painted-wolf/platform/host/coordinator-surfaces.yaml"), &all))
		implement := 0
		for id := range all {
			if strings.HasPrefix(id, "implement_") {
				implement++
				if surface.ExecutionModeFamily(id) == "" {
					r.Report("surface %s maps to no execution-mode family", id)
				}
			}
		}
		r.Expect(implement, "implement surfaces")
	},
}, {
	// SKILL.md frontmatter carries only documented keys.
	Name:    "stock-skill-frontmatter-is-documented",
	Anchors: []invariants.Anchor{invariants.Dir("lycaon/config/packs")},
	Check: func(r *invariants.Run) {
		skills := 0
		for _, f := range r.Tree("lycaon/config/packs", ".md") {
			if !strings.HasSuffix(f.Rel, "/SKILL.md") {
				continue
			}
			skills++
			keys, err := frontmatterKeys(f.Text())
			if err != nil {
				r.Report("%s: %v", f.Rel, err)
				continue
			}
			for _, key := range keys {
				if !contains(skillFrontmatterKeys, key) {
					r.Report("%s: undocumented frontmatter key %q", f.Rel, key)
				}
			}
		}
		r.Expect(skills, "stock skills")
	},
}, {
	// Every role that may wait supports HTTP and port readiness.
	Name:    "wait-roles-publish-readiness-conditions",
	Anchors: []invariants.Anchor{invariants.Func("internal/sandbox", "LoadToolProfiles")},
	Check: func(r *invariants.Run) {
		profiles, err := sandbox.LoadToolProfiles()
		r.Must(err)
		for _, p := range profiles {
			if p.ToolAllowed("wait") && (!contains(p.WaitConditions, "http_ready") || !contains(p.WaitConditions, "port_ready")) {
				r.Report("wait profile %s conditions %v lack http_ready or port_ready", p.ID, p.WaitConditions)
			}
		}
	},
}, {
	// A notice with conditional copy ships a scenario for each branch to render.
	Name:    "conditional-notices-carry-scenarios",
	Anchors: []invariants.Anchor{invariants.Dir(noticesDir)},
	Check: func(r *invariants.Run) {
		for code, entry := range notices(r).UserNotices {
			blob := strings.Join([]string{entry.Title, entry.Message, entry.SuggestedAction}, "\n")
			conditional := strings.Contains(blob, "{% if") || strings.Contains(blob, "{%- if")
			if !entry.UsesDefaults() && entry.IsUserVisible() && conditional && len(entry.Scenarios) == 0 {
				r.Report("notice %s has conditional copy and no scenarios", code)
			}
		}
	},
}}

func frontmatterKeys(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "---") {
		return nil, fmt.Errorf("missing frontmatter")
	}
	rest := trimmed[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated frontmatter")
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(rest[:end]), &doc); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(doc))
	for key := range doc {
		keys = append(keys, key)
	}
	return keys, nil
}
