package toolschema

import (
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

const capabilityRequestArg = "capability_request"

type capabilityFieldCatalog struct {
	Fields map[string]map[string]any `yaml:"fields"`
}

var capabilityFields = sync.OnceValues(func() (map[string]map[string]any, error) {
	data, err := config.Read(config.CapabilityRequestFields)
	if err != nil {
		return nil, fmt.Errorf("read capability_request fields: %w", err)
	}
	var catalog capabilityFieldCatalog
	if err := config.DecodeYAML(data, &catalog); err != nil {
		return nil, fmt.Errorf("parse capability_request fields: %w", err)
	}
	for _, field := range allCapabilityFields() {
		if _, ok := catalog.Fields[field]; !ok {
			return nil, fmt.Errorf("capability_request fields: %q is not defined", field)
		}
	}
	return catalog.Fields, nil
})

// allCapabilityFields is every capability_request field a contract can support.
func allCapabilityFields() []string {
	return toolcontract.Contract{Capabilities: ^toolcontract.Capability(0)}.CapabilityRequestFields()
}

// withCapabilityRequest fills a declared capability_request with the fields the
// tool's execution contract supports. A field the tool declares is merged over
// the shared schema; declaring an unsupported field is an error.
func (e Entry) withCapabilityRequest(name string) (Entry, error) {
	props, _ := e.Schema["properties"].(map[string]any)
	declared, ok := props[capabilityRequestArg].(map[string]any)
	if !ok {
		return e, nil
	}
	shared, err := capabilityFields()
	if err != nil {
		return Entry{}, err
	}
	contract, _ := toolcontract.Lookup(name)
	supported := contract.CapabilityRequestFields()
	narrowed, _ := declared["properties"].(map[string]any)
	for field := range narrowed {
		if !slices.Contains(supported, field) {
			return Entry{}, fmt.Errorf("%s: capability_request.%s is not in the tool's execution contract", name, field)
		}
	}
	fields := make(map[string]any, len(supported))
	for _, field := range supported {
		schema := deepCopy(shared[field])
		if override, ok := narrowed[field].(map[string]any); ok {
			schema = mergeSchema(schema, override)
		}
		fields[field] = schema
	}
	request := maps.Clone(declared)
	request["type"] = "object"
	request["additionalProperties"] = false
	request["properties"] = fields

	out := e
	out.Schema = maps.Clone(e.Schema)
	outProps := maps.Clone(props)
	outProps[capabilityRequestArg] = request
	out.Schema["properties"] = outProps
	return out, nil
}

// mergeSchema overlays override onto base; nested objects merge, other values replace.
func mergeSchema(base, override map[string]any) map[string]any {
	for key, value := range override {
		nested, isMap := value.(map[string]any)
		current, baseIsMap := base[key].(map[string]any)
		if isMap && baseIsMap {
			base[key] = mergeSchema(current, nested)
			continue
		}
		base[key] = deepCopyValue(value)
	}
	return base
}

func deepCopy(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		out[key] = deepCopyValue(value)
	}
	return out
}

func deepCopyValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return deepCopy(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return v
	}
}
