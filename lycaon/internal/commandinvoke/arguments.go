package commandinvoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

const (
	maxArgumentBytes = 128 << 10
	maxStringRunes   = 16 << 10
	maxStringList    = 256
)

// PrepareOutput validates typed output and bounds untyped output.
func PrepareOutput(fields []contribution.OutputField, raw string) (string, error) {
	if len(fields) > 0 {
		if err := ValidateOutput(fields, raw); err != nil {
			return "", err
		}
		return raw, nil
	}
	if len(raw) <= maxArgumentBytes {
		return raw, nil
	}
	budget := maxArgumentBytes - len(runeclamp.TruncatedSuffix)
	return runeclamp.CutBytes(raw, budget) + runeclamp.TruncatedSuffix, nil
}

// ProjectPathResolver resolves a path against attached project roots.
type ProjectPathResolver func(rootID, path string) (resolvedRootID, resolvedPath string, err error)

// ValidateArguments validates and normalizes command arguments.
func ValidateArguments(fields []contribution.InputField, args map[string]any, resolvePath ProjectPathResolver) (map[string]any, error) {
	if args == nil {
		args = map[string]any{}
	}
	encoded, err := json.Marshal(args)
	if err != nil || len(encoded) > maxArgumentBytes {
		return nil, fmt.Errorf("arguments exceed the host size bound")
	}
	declared := make(map[string]contribution.InputField, len(fields))
	for _, field := range fields {
		declared[field.ID] = field
	}
	for key := range args {
		if _, ok := declared[key]; !ok {
			return nil, fmt.Errorf("unknown argument %q", key)
		}
	}
	out := make(map[string]any, len(fields))
	for _, field := range fields {
		value, present := args[field.ID]
		if !present && field.Default != nil {
			value, present = field.Default, true
		}
		if !present {
			if field.Required {
				return nil, fmt.Errorf("argument %q is required", field.ID)
			}
			continue
		}
		normalized, err := validateArgument(field, value, resolvePath)
		if err != nil {
			return nil, fmt.Errorf("argument %q: %w", field.ID, err)
		}
		out[field.ID] = normalized
	}
	return out, nil
}

// ValidateRequiredConfirmations checks active required confirmations.
func ValidateRequiredConfirmations(
	interaction *contribution.Interaction,
	activeFields []contribution.InputField,
	args map[string]any,
) error {
	if interaction == nil {
		return nil
	}
	active := make(map[string]bool, len(activeFields))
	for _, field := range activeFields {
		active[field.ID] = true
	}
	for _, step := range interaction.Steps {
		if active[step.ID] && step.Kind == contribution.InteractionConfirmation && step.Required && args[step.ID] != true {
			return fmt.Errorf("confirmation %s must be accepted", step.ID)
		}
	}
	return nil
}

// ValidateOutput checks a typed result as one strict JSON object.
func ValidateOutput(fields []contribution.OutputField, raw string) error {
	if len(fields) == 0 {
		return nil
	}
	if len(raw) > maxArgumentBytes {
		return fmt.Errorf("operation output exceeds the host size bound")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("operation output must be a JSON object: %w", err)
	}
	converted := contribution.InputFieldsForOutput(fields)
	if _, err := ValidateArguments(converted, value, nil); err != nil {
		return fmt.Errorf("operation output: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("operation output contains trailing data")
	}
	return nil
}

func validateArgument(field contribution.InputField, value any, resolvePath ProjectPathResolver) (any, error) {
	switch field.Type {
	case contribution.PropertyBoolean:
		if _, ok := value.(bool); !ok {
			return nil, fmt.Errorf("must be a boolean")
		}
	case contribution.PropertyString:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		if utf8.RuneCountInString(text) > maxStringRunes {
			return nil, fmt.Errorf("is too long")
		}
	case contribution.PropertyNumber:
		number, ok := jsonNumber(value)
		if !ok || math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, fmt.Errorf("must be a finite number")
		}
		if field.Min != nil && number < *field.Min {
			return nil, fmt.Errorf("is below min")
		}
		if field.Max != nil && number > *field.Max {
			return nil, fmt.Errorf("is above max")
		}
		return number, nil
	case contribution.PropertyEnum:
		member, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		for _, allowed := range field.Values {
			if member == allowed {
				return member, nil
			}
		}
		return nil, fmt.Errorf("must be one of the declared values")
	case contribution.PropertyStringList:
		members, ok := value.([]any)
		if !ok {
			if typed, typedOK := value.([]string); typedOK {
				members = make([]any, len(typed))
				for i := range typed {
					members[i] = typed[i]
				}
			} else {
				return nil, fmt.Errorf("must be a string list")
			}
		}
		if len(members) > maxStringList {
			return nil, fmt.Errorf("has too many members")
		}
		out := make([]string, 0, len(members))
		for _, member := range members {
			text, ok := member.(string)
			if !ok || utf8.RuneCountInString(text) > maxStringRunes {
				return nil, fmt.Errorf("members must be bounded strings")
			}
			if len(field.Values) > 0 && !contains(field.Values, text) {
				return nil, fmt.Errorf("members must be declared values")
			}
			out = append(out, text)
		}
		return out, nil
	case contribution.PropertyProjectPath:
		return resolveProjectPathArgument(value, resolvePath)
	default:
		return nil, fmt.Errorf("has unsupported type %q", field.Type)
	}
	return value, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func resolveProjectPathArgument(value any, resolvePath ProjectPathResolver) (map[string]any, error) {
	var rootID string
	var path string
	switch typed := value.(type) {
	case string:
		path = typed
	case map[string]any:
		rootID, _ = typed["root_id"].(string)
		path, _ = typed["path"].(string)
	default:
		return nil, fmt.Errorf("must be a project path")
	}
	if resolvePath == nil || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("cannot be resolved without an attached project path")
	}
	resolvedRootID, resolvedPath, err := resolvePath(strings.TrimSpace(rootID), strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("is outside the attached roots: %w", err)
	}
	return map[string]any{"root_id": resolvedRootID, "path": resolvedPath}, nil
}

func jsonNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}
