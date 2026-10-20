package toolcontract

import "fmt"

// CommandEffectClass is a compiled operation axis, independent of command names
// and the confinement floor. Permission still requires the executing owner.
type CommandEffectClass string

const (
	CommandReadOnly      CommandEffectClass = "read_only"
	CommandMutation      CommandEffectClass = "mutation"
	CommandExternalExec  CommandEffectClass = "external_exec"
	CommandNetworkEgress CommandEffectClass = "network_egress"
)

// CommandEffects is a set, so a stage requiring both mutation and egress must
// satisfy both restrictions. Zero admits no operation.
type CommandEffects uint8

func commandEffectBit(class CommandEffectClass) CommandEffects {
	switch class {
	case CommandReadOnly:
		return 1
	case CommandMutation:
		return 2
	case CommandExternalExec:
		return 4
	case CommandNetworkEgress:
		return 8
	default:
		return 0
	}
}

func NewCommandEffects(classes ...CommandEffectClass) (CommandEffects, error) {
	var effects CommandEffects
	for _, class := range classes {
		bit := commandEffectBit(class)
		if bit == 0 {
			return 0, fmt.Errorf("unknown command effect class %q", class)
		}
		if effects&bit != 0 {
			return 0, fmt.Errorf("duplicate command effect class %q", class)
		}
		effects |= bit
	}
	return effects, nil
}

// Allows requires every selected effect, not any matching class.
func (allowed CommandEffects) Allows(required CommandEffects) bool {
	return required != 0 && required & ^CommandEffects(15) == 0 && required & ^allowed == 0
}

// Classes returns a detached canonical projection for prompts and rejections.
func (effects CommandEffects) Classes() []CommandEffectClass {
	var classes []CommandEffectClass
	for _, class := range []CommandEffectClass{CommandReadOnly, CommandMutation, CommandExternalExec, CommandNetworkEgress} {
		if effects&commandEffectBit(class) != 0 {
			classes = append(classes, class)
		}
	}
	return classes
}

// CommandEffects compiles the selected catalog mode's requirements. Dynamic
// wrappers require their actual child's bound set; an unresolved child cannot
// be admitted by treating registration or a wrapper's own reads as authority.
func (r EffectRequirement) CommandEffects(child *CommandEffects) (CommandEffects, error) {
	if r.Mode == "" {
		return 0, fmt.Errorf("selected catalog effect mode required")
	}
	var effects CommandEffects
	for _, requirement := range r.Requires {
		switch requirement {
		case "read", "read-request-inputs", "platform-metadata-capability", "session-resource-read", "await-owned-job", "confinement", "recovery", "review", "revision", "secret-disclosure":
			// These obligations remain with the existing execution/effect boundary.
		case "write", "session-owned-process-control":
			effects |= commandEffectBit(CommandMutation)
		case "external-exec":
			effects |= commandEffectBit(CommandExternalExec)
		case "egress":
			effects |= commandEffectBit(CommandNetworkEgress)
		case "bound-child-effect-set":
			if child == nil || *child == 0 || *child & ^CommandEffects(15) != 0 {
				return 0, fmt.Errorf("bound child effect set required")
			}
			effects |= *child
		default:
			return 0, fmt.Errorf("unknown catalog effect requirement %q", requirement)
		}
	}
	if effects & ^commandEffectBit(CommandReadOnly) != 0 {
		effects &^= commandEffectBit(CommandReadOnly)
	} else {
		effects = commandEffectBit(CommandReadOnly)
	}
	return effects, nil
}
