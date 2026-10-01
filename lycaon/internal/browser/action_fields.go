package browser

import (
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/browserengine"
)

// locatorFields name an element; every targeted step reads them.
var locatorFields = []string{"testid", "selector", "role", "label", "text"}

// stepFields lists, per step type, the fields beyond timeout_ms and wait that
// the step reads. timeout_ms and wait apply to every step.
var stepFields = map[string][]string{
	"click":    append(append([]string(nil), locatorFields...), "button", "count", "modifiers"),
	"hover":    append(append([]string(nil), locatorFields...), "hold_ms"),
	"drag":     append(append([]string(nil), locatorFields...), "to", "by", "hold_ms"),
	"scroll":   append(append([]string(nil), locatorFields...), "by"),
	"fill":     append(append([]string(nil), locatorFields...), "value"),
	"type":     append(append([]string(nil), locatorFields...), "value"),
	"select":   append(append([]string(nil), locatorFields...), "value"),
	"press":    {"key"},
	"route":    {"routes"},
	"wait_for": {"selector", "text"},
	"wait":     nil,
}

// setFields lists the step-specific fields a step carries, in schema order.
func (a CaptureAction) setFields() []string {
	var out []string
	add := func(name string, set bool) {
		if set {
			out = append(out, name)
		}
	}
	add("testid", a.Testid != "")
	add("selector", a.Selector != "")
	add("role", a.Role != "")
	add("label", a.Label != "")
	add("text", a.Text != "")
	add("value", a.Value != "")
	add("key", a.Key != "")
	add("modifiers", len(a.Modifiers) > 0)
	add("button", a.Button != "")
	add("count", a.Count != 0)
	add("to", a.To != nil)
	add("by", a.By != nil)
	add("hold_ms", a.HoldMS != 0)
	add("routes", len(a.Routes) > 0)
	return out
}

// validateActions refuses, before any step runs, a batch over the step bound
// or one with a step carrying a field its type does not read.
func validateActions(actions []CaptureAction) error {
	if len(actions) > MaxCaptureActions {
		return browserengine.Reject("CAPTURE_ACTIONS_BOUNDS", map[string]any{"count": len(actions), "max": MaxCaptureActions})
	}
	for i, act := range actions {
		reads, known := stepFields[act.kind()]
		if !known {
			continue
		}
		var unused []string
		for _, field := range act.setFields() {
			if !containsField(reads, field) {
				unused = append(unused, field)
			}
		}
		if len(unused) == 0 {
			continue
		}
		return browserengine.Reject("CAPTURE_ACTION_FIELDS_UNUSED", map[string]any{
			"index":         strconv.Itoa(i),
			"type":          act.kind(),
			"unused_fields": strings.Join(unused, ", "),
			"step_fields":   strings.Join(append(append([]string(nil), reads...), "timeout_ms", "wait"), ", "),
		})
	}
	return nil
}

func containsField(fields []string, name string) bool {
	for _, f := range fields {
		if f == name {
			return true
		}
	}
	return false
}
