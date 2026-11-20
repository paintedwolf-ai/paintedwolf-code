package catalogload

import (
	"go/ast"
	"path"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

var templateVar = regexp.MustCompile(`{{\s*(\w+(?:\.\w+)*)\s*}}`)

// kickTriggerClasses are the structural causes a coordinator kick may fire on.
var kickTriggerClasses = []string{"manifest_on_reenter", "phase_on_enter", "tool_rejection", "host_api", "wait_trigger", "observed_history", "workflow_event", "overlay_state"}

var rejectTemplates = []invariants.Invariant{{
	// A reject template interpolates only facts its literal emission sites
	// supply; anything else renders empty.
	Name:    "reject-template-vars-are-supplied",
	Anchors: []invariants.Anchor{invariants.Func("internal/toolrejection", "CompleteFailureMetadata"), invariants.Func("internal/oarcopy", "FactsFromData")},
	Check: func(r *invariants.Run) {
		emissions := map[string][]map[string]bool{}
		for _, f := range r.GoFiles(invariants.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || invariants.TypeName(lit.Type) != "ToolReject" {
					return true
				}
				var code string
				var data ast.Expr
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						switch invariants.TypeName(kv.Key) {
						case "Code":
							code, _ = invariants.StringLit(kv.Value)
						case "Data":
							data = kv.Value
						}
					}
				}
				keys := map[string]bool{}
				if dm, ok := data.(*ast.CompositeLit); ok {
					for _, elt := range dm.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok {
							if k, ok := invariants.StringLit(kv.Key); ok {
								keys[k] = true
							}
						}
					}
				} else if data != nil {
					return true // data held in a variable cannot be resolved statically
				}
				if code != "" {
					emissions[code] = append(emissions[code], keys)
				}
				return true
			})
		}
		checked := 0
		for _, rel := range r.Glob("lycaon/config/packs/*/*/policy/*.yaml") {
			raw := r.Text(rel)
			code := strings.TrimSuffix(path.Base(rel), ".yaml")
			sites := emissions[code]
			// Control flow may bind or render variables conditionally.
			if strings.Contains(raw, "{%") || len(sites) == 0 {
				continue
			}
			required := map[string]bool{}
			for _, m := range templateVar.FindAllStringSubmatch(raw, -1) {
				required[m[1]] = true
			}
			delete(required, oar.NextStepsFact)
			if len(required) == 0 {
				continue
			}
			checked++
			for _, keys := range sites {
				data := map[string]any{}
				for key := range keys {
					data[key] = "observed"
				}
				facts := oarcopy.FactsFromData(toolrejection.CompleteFailureMetadata(&toolrejection.ToolReject{Data: data}, "invoked_tool", "native").Data)
				for v := range required {
					if _, ok := facts[v]; !ok {
						r.Report("%s: {{ %s }} is not supplied by every emission of %s", rel, v, code)
					}
				}
			}
		}
		r.Expect(checked, "policy templates with literal emission sites")
	},
}, {
	// A coordinator kick fires on a structural cause, never on reading the
	// user's words.
	Name:    "kick-trigger-classes-are-structural",
	Anchors: []invariants.Anchor{invariants.File("lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")},
	Check: func(r *invariants.Run) {
		kicks := 0
		for _, b := range bindings(r).AllInform() {
			if b == nil || !b.IsInform() || !strings.HasPrefix(b.Render, "coordinator-") {
				continue
			}
			kicks++
			id := strings.TrimPrefix(b.Render, "coordinator-")
			class := strings.TrimSpace(b.Invariants.TriggerClass)
			if !contains(kickTriggerClasses, class) {
				r.Report("kick %s has trigger_class %q, not a structural cause", id, class)
			}
			fires := strings.ToLower(strings.Join(b.Invariants.FiresIn, " "))
			for _, banned := range []string{"user message contains", "strings.contains", "phrase list", "chat yes", "lgtm", "nl intent"} {
				if strings.Contains(fires, banned) {
					r.Report("kick %s fires_in names natural-language intent (%s)", id, banned)
				}
			}
		}
		r.Expect(kicks, "coordinator kicks")
	},
}}
