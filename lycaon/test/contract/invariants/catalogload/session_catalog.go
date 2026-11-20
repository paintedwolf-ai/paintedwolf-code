package catalogload

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tooloutput"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

const sessionYAML = "lycaon/config/packs/painted-wolf/platform/host/session.yaml"

// derivedSessionLimits come from the true window, so session.yaml omits them.
var derivedSessionLimits = []string{"max_tool_result_bytes", "max_coordinator_loop_cycles"}

var sessionCatalog = []invariants.Invariant{{
	// Catalog scenario spill paths use the host-data-relative wire form.
	Name:    "catalog-spill-path-fixtures-are-relative",
	Anchors: []invariants.Anchor{invariants.Func("internal/tooloutput", "IsAgentWireSpillRel")},
	Check: func(r *invariants.Run) {
		for _, f := range r.Tree("lycaon/config/packs", ".yaml") {
			var doc any
			r.Must(yaml.Unmarshal(f.Bytes(), &doc))
			obj, _ := doc.(map[string]any)
			scenarios, _ := obj["scenarios"].([]any)
			for _, item := range scenarios {
				entry, _ := item.(map[string]any)
				data, _ := entry["data"].(map[string]any)
				if spill, _ := data["spill_path"].(string); spill != "" && !tooloutput.IsAgentWireSpillRel(spill) {
					r.Report("lycaon/config/packs/%s: spill_path %s is not host-data-relative", f.Rel, spill)
				}
			}
		}
	},
}, {
	// A gate's satisfy arm is live: some bundled phase lists the gate under
	// the matching advance policy.
	Name:    "gate-feedback-satisfy-arms-are-selectable",
	Anchors: []invariants.Anchor{invariants.Func("internal/workflow/definition", "EffectiveAdvancePolicy")},
	Check: func(r *invariants.Run) {
		catalog, err := feedback.LoadGateFeedbackCatalog()
		r.Must(err)
		reg, err := workflowdef.RegistryFromDirs("")
		r.Must(err)
		auto, coordinator := map[string]bool{}, map[string]bool{}
		for _, m := range reg.All() {
			for _, def := range m.PhaseDefs {
				policy := workflowdef.EffectiveAdvancePolicy(m, def)
				for _, leaf := range def.Gates {
					auto[leaf] = auto[leaf] || policy == workflowdef.AdvanceWhenGateMetAuto
					coordinator[leaf] = coordinator[leaf] || policy == workflowdef.AdvanceWhenGateMetCoordinator
				}
			}
		}
		ids := catalog.GateIDs()
		r.Expect(len(ids), "gate feedback entries")
		for _, id := range ids {
			def, _ := catalog.Def(id)
			if len(def.SatisfyAuto) > 0 && !auto[id] {
				r.Report("gate %s has satisfy_auto but no bundled phase advances it automatically", id)
			}
			if len(def.SatisfyCoordinator) > 0 && !coordinator[id] {
				r.Report("gate %s has satisfy_coordinator but no bundled phase lets the coordinator advance it", id)
			}
		}
	},
}, {
	// session.yaml sets exactly the session limits that are not derived.
	Name:    "session-yaml-keys-match-session-limits",
	Anchors: []invariants.Anchor{invariants.Decl("internal/settings", "SessionLimits"), invariants.File(sessionYAML)},
	Check: func(r *invariants.Run) {
		var raw map[string]any
		r.Must(yaml.Unmarshal(r.Bytes(sessionYAML), &raw))
		fields := map[string]bool{}
		typ := reflect.TypeOf(settings.SessionLimits{})
		for i := range typ.NumField() {
			if name, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
				fields[name] = true
			}
		}
		for key := range raw {
			switch {
			case !fields[key]:
				r.Report("session.yaml key %s has no SessionLimits field", key)
			case contains(derivedSessionLimits, key):
				r.Report("session.yaml sets the derived limit %s", key)
			}
		}
		for key := range fields {
			if _, ok := raw[key]; !ok && !contains(derivedSessionLimits, key) {
				r.Report("SessionLimits field %s is missing from session.yaml", key)
			}
		}
	},
}}
