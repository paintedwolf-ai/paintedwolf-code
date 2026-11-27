package oar

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/actionenvelope"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
)

// ToolRoute is how a named tool stands on one frozen request.
type ToolRoute struct {
	Status toolreach.Status
	// Retired reports that native programs replaced the tool.
	Retired bool
	// Forms are those programs; a retired tool may have none.
	Forms []string
}

// RouteTool classifies name against the request a reader acts on.
func RouteTool(reach toolreach.Request, name string) ToolRoute {
	forms, retired := toolcontract.RetiredCommandForms(name)
	return ToolRoute{Status: reach.Status(name), Retired: retired, Forms: forms}
}

func (gc *GuardContext) setObservationFact(name string, value any) {
	if gc.ObservationData == nil {
		gc.ObservationData = map[string]any{}
	}
	gc.ObservationData[name] = value
}

// setTextActionFacts sorts each tool the response named by the route the
// frozen request leaves for it, so recovery copy offers only calls that exist.
func (gc *GuardContext) setTextActionFacts(action actionenvelope.Observation) {
	callable, loadable, forms := []string{}, []string{}, []string{}
	for _, name := range action.Tools {
		route := RouteTool(gc.Reach, name)
		switch route.Status {
		case toolreach.Callable:
			callable = append(callable, name)
			continue
		case toolreach.Loadable:
			loadable = append(loadable, name)
			continue
		case toolreach.Absent:
		}
		for _, form := range route.Forms {
			if gc.Reach.Program(form) && !slices.Contains(forms, form) {
				forms = append(forms, form)
			}
		}
	}
	gc.setObservationFact("offenders_sample", strings.Join(action.Tools, ", "))
	gc.setObservationFact("text_action_fenced", action.Fenced)
	gc.setObservationFact("text_action_callable_tools", callable)
	gc.setObservationFact("text_action_loadable_tools", loadable)
	gc.setObservationFact("text_action_command_forms", forms)
}

// ObserveCommandOperationReach derives command_operation_reachable and
// admitted_command_forms from the occurrence's frozen request reach.
func (gc *GuardContext) ObserveCommandOperationReach() {
	if gc == nil || !gc.Reach.Known() {
		return
	}
	need, _ := gc.ObservationData["need"].(string)
	need = strings.TrimSpace(need)
	if need == "" {
		need, _ = gc.Invocation.ToolArgs["need"].(string)
		need = strings.TrimSpace(need)
	}
	if need == "" {
		gc.setObservationFact("command_operation_reachable", false)
		gc.setObservationFact("admitted_command_forms", []string{})
		return
	}
	route := RouteTool(gc.Reach, need)
	admitted := make([]string, 0, len(route.Forms))
	for _, form := range route.Forms {
		if gc.Reach.Program(form) && !slices.Contains(admitted, form) {
			admitted = append(admitted, form)
		}
	}
	commandReachable := gc.Reach.Status(toolreach.ProgramTool) != toolreach.Absent
	gc.setObservationFact("admitted_command_forms", admitted)
	gc.setObservationFact("command_operation_reachable", commandReachable && len(admitted) > 0)
}
