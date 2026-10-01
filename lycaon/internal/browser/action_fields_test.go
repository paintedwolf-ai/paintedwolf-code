package browser

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
)

func TestValidateActionsNamesFieldsAStepDoesNotRead(t *testing.T) {
	cases := []struct {
		name   string
		act    CaptureAction
		unused string
	}{
		{"click offset", CaptureAction{Type: "click", ActionLocator: ActionLocator{Selector: "#b"}, By: &ActionOffset{X: 1}}, "by"},
		{"hover value", CaptureAction{Type: "hover", ActionLocator: ActionLocator{Selector: "#b"}, Value: "x"}, "value"},
		{"press target", CaptureAction{Type: "press", Key: "Enter", ActionLocator: ActionLocator{Selector: "#b"}}, "selector"},
		{"fill drop target", CaptureAction{Type: "fill", ActionLocator: ActionLocator{Label: "Name"}, Value: "a", To: &ActionLocator{Selector: "#c"}}, "to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateActions([]CaptureAction{tc.act})
			var rej *browserengine.RejectError
			if !errors.As(err, &rej) || rej.Code != "CAPTURE_ACTION_FIELDS_UNUSED" || rej.Data["unused_fields"] != tc.unused {
				t.Fatalf("validate = %v", err)
			}
		})
	}
}

func TestValidateActionsAcceptsEachStepsOwnFields(t *testing.T) {
	err := validateActions([]CaptureAction{
		{Type: "click", ActionLocator: ActionLocator{Role: "button", Label: "Go"}, Button: "right", Count: 2, Modifiers: []string{"Shift"}, TimeoutMS: 500, Wait: "idle"},
		{Type: "drag", ActionLocator: ActionLocator{Selector: "#a"}, By: &ActionOffset{Y: 40}, HoldMS: 100},
		{Type: "scroll", By: &ActionOffset{Y: 300}},
		{Type: "type", Value: "hello"},
		{Type: "press", Key: "Mod+K"},
		{Type: "wait_for", ActionLocator: ActionLocator{Text: "Saved"}},
		{Type: "wait", TimeoutMS: 100},
	})
	if err != nil {
		t.Fatalf("validate = %v", err)
	}
}

func TestValidateActionsKeepsTheStepBound(t *testing.T) {
	err := validateActions(make([]CaptureAction, MaxCaptureActions+1))
	var rej *browserengine.RejectError
	if !errors.As(err, &rej) || rej.Code != "CAPTURE_ACTIONS_BOUNDS" {
		t.Fatalf("validate = %v", err)
	}
}
