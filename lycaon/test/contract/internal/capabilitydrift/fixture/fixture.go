// Package fixture is one disposable registered handler whose owner applies a
// request-time restriction. It qualifies the capability drift checks and is
// never registered by the host.
package fixture

import (
	"context"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	ToolName = "share_report"
	// RefusalCode reuses the existing profile restriction refusal.
	RefusalCode = "COMMAND_EFFECT_NOT_ALLOWED"
	// InstructionID names the delivered instruction that recommends the action.
	InstructionID = "share-weekly-report-with-team"
)

// HostState is the frozen host state one request is decided against.
type HostState struct {
	ID          string
	Recipient   string
	TeamSharing bool
}

// Admission is the owner's typed admission decision for one call.
type Admission struct {
	Admitted bool
}

// Admit decides a call: sharing with the team requires team sharing on the
// recipient's host state.
func Admit(state HostState, args map[string]any) Admission {
	audience, _ := args["audience"].(string)
	return Admission{Admitted: audience != "team" || state.TeamSharing}
}

// Args are the fixed call arguments every stage uses.
func Args() map[string]any {
	return map[string]any{"report": "weekly", "audience": "team"}
}

// Handler is the owner: it revalidates the restriction at request time and
// refuses before any effect.
func Handler(state HostState) tools.ToolHandler {
	return func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		if decision := Admit(state, args); !decision.Admitted {
			return "", &toolrejection.ToolReject{
				Code:         RefusalCode,
				FailureClass: api.FailureClassPolicyRejection,
				BeforeEffect: true,
				Data:         map[string]any{"tool": ToolName, "audience": args["audience"]},
			}
		}
		return "shared", nil
	}
}

// Definition registers the handler under the fixture contract.
func Definition(state HostState) tools.Definition {
	return tools.Definition{
		Meta: tools.ToolMeta{
			Name:        ToolName,
			Description: "Share a report.",
			ArgsSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"report":   map[string]any{"type": "string"},
					"audience": map[string]any{"type": "string", "enum": []any{"self", "team"}},
				},
				"required": []any{"report", "audience"},
			},
		},
		Contract: toolcontract.Contract{Owner: "fixture", Lifecycle: toolcontract.LifecycleEffectAttempt},
		Handler:  Handler(state),
	}
}

// Delivery is what a projection puts on a request: the offered tools and the
// instruction text.
type Delivery struct {
	Tools       []tools.ToolMeta
	Instruction string
}

// Projection builds one request's delivery from the frozen host state.
type Projection func(HostState) Delivery

// StaleProjection advertises the action on every state, ignoring the owner's
// restriction.
func StaleProjection(state HostState) Delivery {
	return offer(state)
}

// AdmittedProjection consumes the owner's typed admission decision, so a
// restricted state neither offers nor recommends the action.
func AdmittedProjection(state HostState) Delivery {
	if !Admit(state, Args()).Admitted {
		return Delivery{}
	}
	return offer(state)
}

func offer(state HostState) Delivery {
	return Delivery{
		Tools:       []tools.ToolMeta{Definition(state).Meta},
		Instruction: "Share the weekly report with the team: " + toolreach.Call{Tool: ToolName, Args: Args()}.Text(),
	}
}
