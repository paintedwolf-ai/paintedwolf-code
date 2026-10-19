package argdiag

import (
	"encoding/json"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

const maxRequiredSummaryBytes = 1024

type requiredSummary struct {
	Missing         []string          `json:"missing_required"`
	Types           map[string]string `json:"expected_types,omitempty"`
	Received        []string          `json:"received_keys"`
	MissingOmitted  int               `json:"missing_required_omitted,omitempty"`
	ReceivedOmitted int               `json:"received_keys_omitted,omitempty"`
}

// MissingRequiredSummary reports actual root-schema failures. Requirements in
// alternative branches and nested objects cannot become unconditional root facts.
func MissingRequiredSummary(invalid *jsonschema.ValidationError, schema, args map[string]any) string {
	if invalid == nil {
		return ""
	}
	missing := rootMissingRequired(invalid)
	if len(missing) == 0 {
		return ""
	}
	slices.Sort(missing)
	out := requiredSummary{Missing: []string{}, Received: []string{}, Types: map[string]string{}}
	properties, _ := schema["properties"].(map[string]any)
	for _, name := range missing {
		if len(out.Missing) == 8 || len(name) > 128 {
			out.MissingOmitted++
			continue
		}
		out.Missing = append(out.Missing, name)
		property, _ := properties[name].(map[string]any)
		if _, reference := property["$ref"]; reference {
			continue
		}
		if typ, _ := property["type"].(string); slices.Contains([]string{"string", "object", "array", "number", "integer", "boolean", "null"}, typ) {
			out.Types[name] = typ
		}
	}
	for _, name := range sortedArgKeys(args) {
		if len(out.Received) == 8 || len(name) > 128 {
			out.ReceivedOmitted++
			continue
		}
		out.Received = append(out.Received, name)
	}
	for len(out.Missing) > 0 {
		encoded, _ := json.Marshal(out)
		if len(encoded) <= maxRequiredSummaryBytes {
			return string(encoded)
		}
		if n := len(out.Received); n > 0 {
			out.Received = out.Received[:n-1]
			out.ReceivedOmitted++
		} else {
			n := len(out.Missing)
			delete(out.Types, out.Missing[n-1])
			out.Missing = out.Missing[:n-1]
			out.MissingOmitted++
		}
	}
	return ""
}

func rootMissingRequired(root *jsonschema.ValidationError) []string {
	pending := []*jsonschema.ValidationError{root}
	for visited := 0; len(pending) > 0 && visited < 32; visited++ {
		e := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if e.SchemaURL != root.SchemaURL || len(e.InstanceLocation) != 0 {
			continue
		}
		if required, ok := e.ErrorKind.(*kind.Required); ok {
			return slices.Clone(required.Missing)
		}
		for _, child := range e.Causes[:min(len(e.Causes), 32-len(pending))] {
			pending = append(pending, child)
		}
	}
	return nil
}
