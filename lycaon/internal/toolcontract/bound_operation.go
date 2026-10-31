package toolcontract

import "fmt"

// BoundOperation is immutable effect metadata for one reached native child.
// It grants no capability, review, revision base or authority to descendants.
type BoundOperation struct {
	definition  OperationDefinition
	invocation  OperationInvocation
	command     string
	mode        string
	effects     CommandEffects
	publication PublicationKind
}

func BindOperation(definition OperationDefinition, invocation OperationInvocation, command, mode string, child *CommandEffects) (BoundOperation, error) {
	if err := definition.ValidateNativeCommands(); err != nil {
		return BoundOperation{}, err
	}
	if err := invocation.Validate(); err != nil {
		return BoundOperation{}, err
	}
	facts := definition.ReadOnlyFacts()
	if invocation.Kind != ProgramChildInvocation || invocation.CatalogDigest != facts.CatalogDigest {
		return BoundOperation{}, fmt.Errorf("native binding requires matching program child catalog")
	}
	for _, spec := range facts.Commands {
		if spec.Name != command {
			continue
		}
		for _, requirement := range spec.Effects {
			if requirement.Mode == mode {
				effects, err := requirement.CommandEffects(child)
				if err != nil {
					return BoundOperation{}, err
				}
				if child != nil {
					if *child == 0 || *child & ^CommandEffects(15) != 0 {
						return BoundOperation{}, fmt.Errorf("valid composed effect set required")
					}
					effects |= *child
					if effects & ^commandEffectBit(CommandReadOnly) != 0 {
						effects &^= commandEffectBit(CommandReadOnly)
					}
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

// PublicationKind identifies host preparation and syntax redirects independently
// of lexical utility commands. These are reached write-owner bindings.
type PublicationKind uint8

const (
	NoPublication PublicationKind = iota
	RedirectPublication
	AcceptedDocumentPublication
)

func BindPublication(definition OperationDefinition, invocation OperationInvocation, kind PublicationKind, operator string) (BoundOperation, error) {
	if err := definition.ValidateNativeCommands(); err != nil {
		return BoundOperation{}, err
	}
	if err := invocation.Validate(); err != nil {
		return BoundOperation{}, err
	}
	if invocation.Kind != ProgramChildInvocation || invocation.CatalogDigest != definition.Facts().CatalogDigest {
		return BoundOperation{}, fmt.Errorf("publication requires a matching write owner")
	}
	mutation, _ := NewCommandEffects(CommandMutation)
	writes := false
	for _, command := range definition.Facts().Commands {
		for _, requirement := range command.Effects {
			effects, err := requirement.CommandEffects(nil)
			writes = writes || err == nil && effects.Allows(mutation)
		}
	}
	if !writes {
		return BoundOperation{}, fmt.Errorf("publication requires a declared mutation owner")
	}
	command := definition.Facts().Identity
	switch kind {
	case RedirectPublication:
		switch operator {
		case ">", ">>", "&>", "&>>":
		default:
			return BoundOperation{}, fmt.Errorf("output redirect operator required")
		}
		command = operator
	case AcceptedDocumentPublication:
		if operator != "" {
			return BoundOperation{}, fmt.Errorf("document publication has no redirect operator")
		}
	default:
		return BoundOperation{}, fmt.Errorf("publication kind required")
	}
	effects, _ := NewCommandEffects(CommandMutation)
	return BoundOperation{definition: definition, invocation: invocation, command: command, mode: "publication", effects: effects, publication: kind}, nil
}
func (b BoundOperation) Publication() PublicationKind { return b.publication }
