package extpacks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// NormalizePolicyDocument decodes JSON surrogate pairs for the YAML parser.
// Numeric tokens and duplicate keys remain intact for validation.
func NormalizePolicyDocument(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return data, nil
	}
	var out bytes.Buffer
	for i := 0; i < len(data); {
		if data[i] != '"' {
			out.WriteByte(data[i])
			i++
			continue
		}
		start := i
		i++
		for data[i] != '"' {
			if data[i] == '\\' {
				i++
			}
			i++
		}
		i++
		var value string
		if err := json.Unmarshal(data[start:i], &value); err != nil {
			return nil, fmt.Errorf("decode policy string: %w", err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode policy string: %w", err)
		}
		out.Write(encoded)
	}
	return out.Bytes(), nil
}

// PolicyDocumentIdentity reads the document identity independently of storage.
// Full OAR shape and capability validation belongs to the production loader.
func PolicyDocumentIdentity(data []byte) (identity string, mandatory, oar bool, err error) {
	data, err = NormalizePolicyDocument(data)
	if err != nil {
		return
	}
	var doc map[string]any
	if err = yaml.Unmarshal(data, &doc); err != nil {
		return
	}
	if _, oar = doc["oar"]; !oar {
		return
	}
	id, _ := doc["id"].(string)
	if id == "" {
		err = fmt.Errorf("[OAR-DOC-6] policy document requires id")
		return
	}
	namespace, _ := doc["namespace"].(string)
	identity = id
	if namespace != "" {
		identity = namespace + "/" + id
	}
	mandatory, _ = doc["mandatory"].(bool)
	return
}

// ValidateOARPolicies examines every admitted contribution before winner and
// disable filtering can hide a duplicate or remove a mandatory rule.
func (e *EffectiveCatalog) ValidateOARPolicies() error {
	ids := make([]string, 0, len(e.Units))
	for id := range e.Units {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seen := map[string]Source{}
	for _, id := range ids {
		unit := e.Units[id]
		if KindRootForUnitID(id) != "policy" {
			continue
		}
		for _, source := range unit.Contributions {
			identity, mandatory, oar, err := PolicyDocumentIdentity(source.Content)
			if err != nil {
				return fmt.Errorf("%s: %w", source.Path, err)
			}
			if !oar {
				continue
			}
			if previous, exists := seen[identity]; exists {
				return fmt.Errorf("[OAR-DOC-9] duplicate rule identity %s (%s and %s)", identity, previous, source.Path)
			}
			seen[identity] = source.Path
			if mandatory && unit.Status == UnitStatusDisabled {
				return fmt.Errorf("[OAR-OPS-7] mandatory rule %s cannot be disabled", identity)
			}
		}
	}
	return nil
}
