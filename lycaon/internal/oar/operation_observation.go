package oar

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ObserveNativeOperation supplements an actual command call with its reached
// operation. Tool and tool_args retain the submitted model-call meaning.
func (gc *GuardContext) ObserveNativeOperation(operation toolcontract.BoundOperation) error {
	if gc == nil || gc.Invocation.Tool != "command" {
		return fmt.Errorf("native observation requires actual command tool identity")
	}
	if err := operation.Invocation().Validate(); err != nil {
		return err
	}
	if operation.Invocation().Kind != toolcontract.ProgramChildInvocation {
		return fmt.Errorf("native observation requires a program child")
	}
	if err := operation.Definition().ValidateNativeCommands(); err != nil {
		return err
	}
	if gc.Published == nil {
		gc.Published = map[string]any{}
	}
	// Canonical host observations take precedence over presentation details.
	gc.Published["paintedwolf.native_operation"] = operation.Definition().Facts().Identity
	return nil
}
