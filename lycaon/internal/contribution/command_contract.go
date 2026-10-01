package contribution

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxInputFields       = 32
	MaxInteractionSteps  = 16
	MaxChoicesPerStep    = 256
	MaxSearchResults     = 100
	DefaultSearchResults = 40
)

func validateInputFields(label string, fields []InputField, requireTitles bool) error {
	if len(fields) > MaxInputFields {
		return fmt.Errorf("%s has %d fields, over the limit of %d", label, len(fields), MaxInputFields)
	}
	seen := map[string]bool{}
	for index := range fields {
		field := &fields[index]
		if !nameGrammar.MatchString(field.ID) {
			return fmt.Errorf("%s field id %q must be lowercase kebab-case", label, field.ID)
		}
		if seen[field.ID] {
			return fmt.Errorf("%s lists field %q twice", label, field.ID)
		}
		seen[field.ID] = true
		if requireTitles && strings.TrimSpace(field.Title) == "" {
			return fmt.Errorf("%s field %s: title is required", label, field.ID)
		}
		if !propertyTypes[field.Type] {
			return fmt.Errorf("%s field %s: unknown type %q", label, field.ID, field.Type)
		}
		if err := validateEnumAndBounds(label+" field "+field.ID, field.Type, field.Values, field.Min, field.Max); err != nil {
			return err
		}
		if field.Default != nil {
			if err := checkPropertyValue("default", field.Type, field.Values, field.Min, field.Max, field.Default); err != nil {
				return fmt.Errorf("%s field %s: %w", label, field.ID, err)
			}
		}
	}
	return nil
}

// InputFieldsForOutput reuses argument validation for typed output.
func InputFieldsForOutput(fields []OutputField) []InputField {
	out := make([]InputField, 0, len(fields))
	for _, field := range fields {
		out = append(out, InputField{
			ID: field.ID, Type: field.Type, Required: field.Required,
			Values: append([]string(nil), field.Values...), Min: field.Min, Max: field.Max,
		})
	}
	return out
}

func validateResult(kind ActionKind, result ResultTreatment) error {
	switch kind {
	case ActionMCPTool:
		if result == "" || result == ResultOutput || result == ResultDiscard {
			return nil
		}
	case ActionOperation:
		if result == "" {
			return nil
		}
	case ActionComposerPrefill, ActionNavigate, ActionExternalLink, ActionNativeUI:
		if result == "" || result == ResultEffect {
			return nil
		}
	case ActionWorkflowStart, ActionEditorAction:
		if result == "" || result == ResultReceipt {
			return nil
		}
	}
	return fmt.Errorf("action %s does not support result treatment %q", kind, result)
}

// DefaultResultFor returns an action's result sink.
func DefaultResultFor(kind ActionKind) ResultTreatment {
	switch kind {
	case ActionMCPTool:
		return ResultOutput
	case ActionWorkflowStart, ActionEditorAction:
		return ResultReceipt
	default:
		return ResultEffect
	}
}

// DefaultIconFor returns an action's semantic icon.
func DefaultIconFor(kind ActionKind) string {
	switch kind {
	case ActionEditorAction:
		return "edit"
	case ActionWorkflowStart:
		return "route"
	case ActionMCPTool:
		return "tool"
	case ActionComposerPrefill:
		return "edit"
	case ActionNavigate:
		return "launcher"
	case ActionExternalLink:
		return "link"
	default:
		return "play"
	}
}

func validateInteraction(interaction *Interaction) error {
	if interaction == nil {
		return nil
	}
	if len(interaction.Steps) == 0 {
		return fmt.Errorf("interaction.steps is required")
	}
	if len(interaction.Steps) > MaxInteractionSteps {
		return fmt.Errorf("interaction has %d steps, over the limit of %d", len(interaction.Steps), MaxInteractionSteps)
	}
	prior := map[string]InteractionStep{}
	for index, step := range interaction.Steps {
		label := fmt.Sprintf("interaction step %d", index+1)
		if !nameGrammar.MatchString(step.ID) || prior[step.ID].ID != "" {
			return fmt.Errorf("%s id %q is invalid or duplicated", label, step.ID)
		}
		if strings.TrimSpace(step.Title) == "" {
			return fmt.Errorf("%s title is required", label)
		}
		if !interactionKinds[step.Kind] {
			return fmt.Errorf("%s has unknown kind %q", label, step.Kind)
		}
		if len(step.Values) > MaxChoicesPerStep {
			return fmt.Errorf("%s has too many choices", label)
		}
		if step.Kind != InteractionChoice && (len(step.Values) > 0 || step.Source != nil || step.Multiple) {
			return fmt.Errorf("%s choice fields only apply to kind choice", label)
		}
		if step.Kind == InteractionChoice {
			if (len(step.Values) == 0) == (step.Source == nil) {
				return fmt.Errorf("%s requires exactly one of values or source", label)
			}
			seenChoices := map[string]bool{}
			for _, choice := range step.Values {
				if strings.TrimSpace(choice.ID) == "" || strings.TrimSpace(choice.Label) == "" || seenChoices[choice.ID] {
					return fmt.Errorf("%s choices require unique non-empty id and label", label)
				}
				seenChoices[choice.ID] = true
				if !boundedText(choice.ID, 256) || !boundedText(choice.Label, 256) || !boundedText(choice.Description, 1024) || !boundedText(choice.Detail, 2048) {
					return fmt.Errorf("%s choice text exceeds its host bound", label)
				}
			}
		}
		if step.Kind != InteractionNumber && (step.Min != nil || step.Max != nil) {
			return fmt.Errorf("%s bounds only apply to number", label)
		}
		if step.Min != nil && step.Max != nil && *step.Min > *step.Max {
			return fmt.Errorf("%s min exceeds max", label)
		}
		if step.Source != nil {
			if step.Source.Kind != ChoiceSourceMCP || strings.TrimSpace(step.Source.Requirement) == "" || strings.TrimSpace(step.Source.Tool) == "" {
				return fmt.Errorf("%s source must name an mcp requirement and tool", label)
			}
			for arg, answerID := range step.Source.Inputs {
				if strings.TrimSpace(arg) == "" || prior[answerID].ID == "" {
					return fmt.Errorf("%s source input %q must name an earlier answer", label, arg)
				}
			}
		}
		if step.If != nil {
			dependency, ok := prior[step.If.Step]
			if !ok {
				return fmt.Errorf("%s condition must name an earlier answer", label)
			}
			if err := checkInteractionValue("condition", dependency, step.If.Is); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		}
		prior[step.ID] = step
	}
	return nil
}

func checkInteractionValue(label string, step InteractionStep, value any) error {
	switch step.Kind {
	case InteractionString, InteractionProjectPath:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", label)
		}
	case InteractionNumber:
		if _, ok := asNumber(value); !ok {
			return fmt.Errorf("%s must be a number", label)
		}
	case InteractionBoolean, InteractionConfirmation:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", label)
		}
	case InteractionChoice:
		if step.Multiple {
			if _, ok := value.([]any); !ok {
				return fmt.Errorf("%s must be a list", label)
			}
		} else if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", label)
		}
	}
	return nil
}

func boundedText(value string, max int) bool { return utf8.RuneCountInString(value) <= max }

func validateSearchSource(source *SearchSource) error {
	if strings.TrimSpace(source.Label) == "" {
		return fmt.Errorf("label is required")
	}
	if !nameGrammar.MatchString(source.Prefix) {
		return fmt.Errorf("prefix must be lowercase kebab-case")
	}
	if strings.TrimSpace(source.Requirement) == "" || strings.TrimSpace(source.Tool) == "" {
		return fmt.Errorf("requirement and tool are required")
	}
	if source.Query.MinLength < 0 || source.Query.MinLength > 256 {
		return fmt.Errorf("query.min_length is out of range")
	}
	if source.Query.MaxResults < 0 || source.Query.MaxResults > MaxSearchResults {
		return fmt.Errorf("query.max_results is out of range")
	}
	if source.Query.MaxResults == 0 {
		source.Query.MaxResults = DefaultSearchResults
	}
	if strings.TrimSpace(source.Result.ID) == "" || strings.TrimSpace(source.Result.Title) == "" {
		return fmt.Errorf("result.id and result.title are required")
	}
	if !searchResultKinds[source.Result.Kind] {
		return fmt.Errorf("unknown result kind %q", source.Result.Kind)
	}
	if strings.TrimSpace(source.Activation.Command) == "" {
		return fmt.Errorf("activation.command is required")
	}
	if err := validateOutputFields("activation input", source.Activation.Input); err != nil {
		return err
	}
	return nil
}

func validateOperation(operation *Operation, stock bool) error {
	if err := validateInputFields("operation input", operation.Input, false); err != nil {
		return err
	}
	if len(operation.Input) > 0 && operation.Action.Kind != ActionMCPTool && operation.Action.Kind != ActionEditorAction && operation.Action.Kind != ActionOperation {
		return fmt.Errorf("operation action %s does not consume input", operation.Action.Kind)
	}
	if operation.Action.Kind == ActionEditorAction {
		for _, field := range operation.Input {
			if field.ID != "instruction" || field.Type != PropertyString {
				return fmt.Errorf("editor_action operation input supports only the string field instruction")
			}
		}
	}
	if err := validateOutputFields("operation output", operation.Output); err != nil {
		return err
	}
	if len(operation.Output) > 0 && operation.Action.Kind != ActionMCPTool && operation.Action.Kind != ActionOperation {
		return fmt.Errorf("operation action %s does not produce typed output", operation.Action.Kind)
	}
	if err := validateAction(operation.Action, stock); err != nil {
		return err
	}
	if err := validateResult(operation.Action.Kind, operation.Result); err != nil {
		return err
	}
	return nil
}

func validateOutputFields(label string, fields []OutputField) error {
	if len(fields) > MaxInputFields {
		return fmt.Errorf("%s has %d fields, over the limit of %d", label, len(fields), MaxInputFields)
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if !nameGrammar.MatchString(field.ID) || seen[field.ID] {
			return fmt.Errorf("%s field %q is invalid or duplicated", label, field.ID)
		}
		seen[field.ID] = true
		if !propertyTypes[field.Type] {
			return fmt.Errorf("%s field %s: unknown type %q", label, field.ID, field.Type)
		}
		if field.Type == PropertyProjectPath {
			return fmt.Errorf("%s field %s: project_path is not legal in untrusted output", label, field.ID)
		}
		if err := validateEnumAndBounds(label+" field "+field.ID, field.Type, field.Values, field.Min, field.Max); err != nil {
			return err
		}
	}
	return nil
}

func sameInputSchema(a, b []InputField) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		x, y := a[index], b[index]
		if x.ID != y.ID || x.Type != y.Type || x.Required != y.Required ||
			strings.Join(x.Values, "\x00") != strings.Join(y.Values, "\x00") || !sameFloat(x.Min, y.Min) || !sameFloat(x.Max, y.Max) {
			return false
		}
	}
	return true
}

func sameOutputSchema(a, b []OutputField) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		x, y := a[index], b[index]
		if x.ID != y.ID || x.Type != y.Type || x.Required != y.Required ||
			strings.Join(x.Values, "\x00") != strings.Join(y.Values, "\x00") || !sameFloat(x.Min, y.Min) || !sameFloat(x.Max, y.Max) {
			return false
		}
	}
	return true
}

func outputMatchesInput(output []OutputField, input []InputField) bool {
	if len(output) != len(input) {
		return false
	}
	for index := range output {
		x, y := output[index], input[index]
		if x.ID != y.ID || x.Type != y.Type || x.Required != y.Required ||
			strings.Join(x.Values, "\x00") != strings.Join(y.Values, "\x00") || !sameFloat(x.Min, y.Min) || !sameFloat(x.Max, y.Max) {
			return false
		}
	}
	return true
}

func sameFloat(a, b *float64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
