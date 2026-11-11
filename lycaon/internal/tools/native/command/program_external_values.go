package command

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// External values keep process-substitution handles outside secret substitution.
func externalValueArguments(request commandprogram.OperationRequest, line, cwd string) map[string]any {
	fields := externalFields(request.Argv)
	values := []string{}
	for _, field := range fields[1:] {
		parts := field.Parts()
		if len(parts) == 0 {
			values = append(values, field.Value)
		} else {
			for _, part := range parts {
				if part.Substitution == nil {
					values = append(values, part.Value)
				}
			}
		}
	}
	names, environment := []string{}, []string{}
	for _, entry := range request.Environment {
		name, value, _ := strings.Cut(entry, "=")
		names, environment = append(names, name), append(environment, value)
	}
	return map[string]any{"command": line, "cwd": cwd, "executable": fields[0].Value,
		"argument_values": values, "environment_names": names, "environment_values": environment}
}

func externalResolvedRequest(request commandprogram.OperationRequest, resolved map[string]any) (commandprogram.OperationRequest, error) {
	values, err := externalStrings(resolved["argument_values"])
	if err != nil {
		return request, err
	}
	environment, err := externalStrings(resolved["environment_values"])
	if err != nil {
		return request, err
	}
	if len(environment) != len(request.Environment) {
		return request, fmt.Errorf("external environment shape changed after review")
	}
	fields := externalFields(request.Argv)
	if resolved["executable"] != fields[0].Value {
		return request, fmt.Errorf("external executable changed after review")
	}
	next := 0
	take := func() (string, error) {
		if next >= len(values) {
			return "", fmt.Errorf("external argument shape changed after review")
		}
		value := values[next]
		next++
		return value, nil
	}
	request.Argv = append(commandprogram.Fields(nil), request.Argv...)
	offset := len(request.Argv) - len(fields)
	for i, field := range fields[1:] {
		parts := field.Parts()
		if len(parts) == 0 {
			field.Value, err = take()
		} else {
			parts = append([]commandprogram.FieldPart(nil), parts...)
			var value strings.Builder
			for j := range parts {
				if parts[j].Substitution == nil {
					parts[j].Value, err = take()
				}
				if err != nil {
					return request, err
				}
				value.WriteString(parts[j].Value)
			}
			field = commandprogram.NewFieldWithParts(value.String(), field.Span, parts)
		}
		if err != nil {
			return request, err
		}
		request.Argv[offset+i+1] = field
	}
	if next != len(values) {
		return request, fmt.Errorf("external argument shape changed after review")
	}
	request.Environment = append([]string(nil), request.Environment...)
	for i, entry := range request.Environment {
		name, _, _ := strings.Cut(entry, "=")
		request.Environment[i] = name + "=" + environment[i]
	}
	return request, nil
}

func externalStrings(value any) ([]string, error) {
	switch values := value.(type) {
	case []string:
		return values, nil
	case []any:
		out := make([]string, len(values))
		for i, value := range values {
			var ok bool
			out[i], ok = value.(string)
			if !ok {
				return nil, fmt.Errorf("external value is not a string")
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("external values are not an array")
	}
}
