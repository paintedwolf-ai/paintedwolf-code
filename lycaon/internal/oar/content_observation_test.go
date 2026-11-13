package oar

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/actionenvelope"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

// retiredWithForms returns a command-replaced tool and the programs that replaced it.
func retiredWithForms(t *testing.T) (string, []string) {
	t.Helper()
	names, err := toolcontract.CommandOperations()
	if err != nil {
		t.Fatalf("command operations: %v", err)
	}
	for _, name := range names {
		if forms, retired := toolcontract.RetiredCommandForms(name); retired && len(forms) > 0 {
			return name, forms
		}
	}
	t.Fatal("no command-replaced tool declares a native form")
	return "", nil
}

func TestTextActionFactsFollowTheFrozenRequest(t *testing.T) {
	retired, forms := retiredWithForms(t)
	offered := []string{"command", toolreach.LoaderTool}
	plan := toolsurface.Compile(offered, []string{"task"})
	runs := func(allowed ...string) toolreach.Programs {
		return func(name string) bool {
			for _, form := range allowed {
				if form == name {
					return true
				}
			}
			return false
		}
	}
	action := actionenvelope.Observation{Tools: []string{"command", "task", retired, "nowhere"}, Fenced: true}
	for _, tc := range []struct {
		name      string
		reach     toolreach.Request
		callable  []string
		loadable  []string
		wantForms []string
	}{
		{"every route open", toolreach.Freeze(offered, plan).WithPrograms(runs(forms...)), []string{"command"}, []string{"task"}, forms},
		{"one program admitted", toolreach.Freeze(offered, plan).WithPrograms(runs(forms[0])), []string{"command"}, []string{"task"}, forms[:1]},
		{"no program admitted", toolreach.Freeze(offered, plan).WithPrograms(runs()), []string{"command"}, []string{"task"}, []string{}},
		{"loader not offered", toolreach.Freeze([]string{"command"}, plan).WithPrograms(runs(forms...)), []string{"command"}, []string{}, forms},
		{"unknown request", toolreach.Request{}, []string{}, []string{}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc := NewGuardContext()
			gc.Reach = tc.reach
			gc.ObservationData = map[string]any{"offered_tools": tc.reach.OfferedNames()}
			gc.SetContentObservation(ContentObservation{TextAction: action})
			if gc.Rejection.RejectObservation != "text_action_envelope" {
				t.Fatalf("observation = %q", gc.Rejection.RejectObservation)
			}
			for name, want := range map[string]any{
				"text_action_fenced":         true,
				"text_action_callable_tools": tc.callable,
				"text_action_loadable_tools": tc.loadable,
				"text_action_command_forms":  tc.wantForms,
			} {
				if got := gc.ObservationData[name]; !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %#v, want %#v", name, got, want)
				}
			}
			if _, kept := gc.ObservationData["offered_tools"]; !kept {
				t.Error("an earlier observation fact was dropped")
			}
		})
	}
}

func TestRouteToolMatchesTheFrozenRequest(t *testing.T) {
	retired, forms := retiredWithForms(t)
	reach := toolreach.Freeze([]string{"command", toolreach.LoaderTool}, toolsurface.Compile([]string{"command", toolreach.LoaderTool}, []string{"task"}))
	for name, want := range map[string]ToolRoute{
		"command": {Status: toolreach.Callable},
		"task":    {Status: toolreach.Loadable},
		retired:   {Status: toolreach.Absent, Retired: true, Forms: forms},
		"nowhere": {Status: toolreach.Absent},
	} {
		if got := RouteTool(reach, name); !reflect.DeepEqual(got, want) {
			t.Errorf("%s route = %+v, want %+v", name, got, want)
		}
	}
}
