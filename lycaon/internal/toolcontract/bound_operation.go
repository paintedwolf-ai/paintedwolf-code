package toolcontract

import "fmt"

// BoundOperation is immutable effect metadata for one reached native child.
// It grants no capability, review, revision base or authority to descendants.
type BoundOperation struct {
	definition OperationDefinition
	invocation OperationInvocation
	command    string
	mode       string
	effects    CommandEffects
}

func BindOperation(definition OperationDefinition, invocation OperationInvocation, command, mode string, child *CommandEffects) (BoundOperation, error) {
	if err := definition.ValidateNativeCommands(); err != nil {
		return BoundOperation{}, err
	}
	if err := invocation.Validate(); err != nil {
		return BoundOperation{}, err
	}
	if invocation.Kind != ProgramChildInvocation || invocation.CatalogDigest != definition.Facts().CatalogDigest {
		return BoundOperation{}, fmt.Errorf("native binding requires matching program child catalog")
	}
	for _, spec := range definition.Facts().Commands {
		if spec.Name != command {
			continue
		}
		for _, requirement := range spec.Effects {
			if requirement.Mode == mode {
				effects, err := requirement.CommandEffects(child)
				if err != nil {
					return BoundOperation{}, err
				}
				return BoundOperation{definition: definition, invocation: invocation, command: command, mode: mode, effects: effects}, nil
			}
		}
	}
	return BoundOperation{}, fmt.Errorf("native binding requires exact declared command and effect mode")
}

func (b BoundOperation) Definition() OperationDefinition { return b.definition }
func (b BoundOperation) Invocation() OperationInvocation { return b.invocation }
func (b BoundOperation) Command() string                 { return b.command }
func (b BoundOperation) Mode() string                    { return b.mode }
func (b BoundOperation) Effects() CommandEffects         { return b.effects }
