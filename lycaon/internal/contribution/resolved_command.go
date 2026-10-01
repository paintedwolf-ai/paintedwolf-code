package contribution

import "reflect"

// ResolvedCommand is the executor contract after operation resolution.
type ResolvedCommand struct {
	Action Action
	Input  []InputField
	Output []OutputField
	Result ResultTreatment
	Icon   string
	Chain  []ID
}

// ResolveCommand follows the already-validated acyclic operation graph.
func (s *Set) ResolveCommand(command *Command) (ResolvedCommand, bool) {
	if s == nil || command == nil {
		return ResolvedCommand{}, false
	}
	resolved := ResolvedCommand{
		Action: command.Action,
		Input:  append([]InputField(nil), command.Input...),
		Result: command.Result,
		Icon:   command.Icon,
	}
	if command.Interaction != nil {
		resolved.Input = InteractionFields(command.Interaction)
	}
	if resolved.Icon == "" {
		resolved.Icon = DefaultIconFor(resolved.Action.Kind)
	}
	seen := map[ID]bool{}
	for resolved.Action.Kind == ActionOperation {
		ref, err := ParseID(resolved.Action.Ref)
		if err != nil || seen[ref] {
			return ResolvedCommand{}, false
		}
		seen[ref] = true
		operation, ok := s.operations[ref]
		if !ok {
			return ResolvedCommand{}, false
		}
		resolved.Chain = append(resolved.Chain, ref)
		if resolved.Output == nil {
			resolved.Output = append([]OutputField(nil), operation.Output...)
		}
		if len(resolved.Input) == 0 {
			resolved.Input = append([]InputField(nil), operation.Input...)
		}
		resolved.Action = operation.Action
		if operation.Result != "" {
			resolved.Result = operation.Result
		}
	}
	if resolved.Result == "" {
		resolved.Result = DefaultResultFor(resolved.Action.Kind)
	}
	if command.Icon == "" {
		resolved.Icon = DefaultIconFor(resolved.Action.Kind)
	}
	return resolved, true
}

// InteractionFields returns the final answer schema of an interaction.
func InteractionFields(interaction *Interaction) []InputField {
	if interaction == nil {
		return nil
	}
	out := make([]InputField, 0, len(interaction.Steps))
	for _, step := range interaction.Steps {
		field := InputField{ID: step.ID, Title: step.Title, Description: step.Description, Required: step.Required, Min: step.Min, Max: step.Max}
		switch step.Kind {
		case InteractionString:
			field.Type = PropertyString
		case InteractionNumber:
			field.Type = PropertyNumber
		case InteractionBoolean, InteractionConfirmation:
			field.Type = PropertyBoolean
		case InteractionProjectPath:
			field.Type = PropertyProjectPath
		case InteractionChoice:
			if step.Multiple {
				field.Type = PropertyStringList
			} else if step.Source != nil {
				field.Type = PropertyString
			} else {
				field.Type = PropertyEnum
			}
			for _, choice := range step.Values {
				field.Values = append(field.Values, choice.ID)
			}
		}
		out = append(out, field)
	}
	return out
}

// InteractionFieldsForAnswers projects steps enabled by prior answers.
func InteractionFieldsForAnswers(interaction *Interaction, answers map[string]any) []InputField {
	if interaction == nil {
		return nil
	}
	active := &Interaction{Steps: make([]InteractionStep, 0, len(interaction.Steps))}
	for _, step := range interaction.Steps {
		if step.If != nil && !interactionValueEqual(answers[step.If.Step], step.If.Is) {
			continue
		}
		active.Steps = append(active.Steps, step)
	}
	return InteractionFields(active)
}

func interactionValueEqual(a, b any) bool {
	if left, ok := asNumber(a); ok {
		right, rightOK := asNumber(b)
		return rightOK && left == right
	}
	return reflect.DeepEqual(a, b)
}
