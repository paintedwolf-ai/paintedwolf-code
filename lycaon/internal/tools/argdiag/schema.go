package argdiag

import (
	"sort"
)

// declaredProperties merges an object schema's properties with those of its
// combinator branches.
func declaredProperties(schema map[string]any) map[string]map[string]any {
	if schema == nil {
		return nil
	}
	props := map[string]map[string]any{}
	addProps := func(p any) {
		if m, ok := p.(map[string]any); ok {
			for k, v := range m {
				if vm, ok := v.(map[string]any); ok {
					props[k] = vm
				} else {
					props[k] = map[string]any{}
				}
			}
		}
	}
	if p, ok := schema["properties"]; ok {
		addProps(p)
	}
	for _, comb := range []string{"oneOf", "anyOf", "allOf"} {
		if list, ok := schema[comb].([]any); ok {
			for _, item := range list {
				if branch, ok := item.(map[string]any); ok {
					if p, ok := branch["properties"]; ok {
						addProps(p)
					}
				}
			}
		}
	}
	return props
}

func detectCombinatorConflicts(schema map[string]any, args map[string]any) []string {
	branches, ok := schema["oneOf"].([]any)
	if !ok || len(branches) < 2 {
		return nil
	}
	var presentBranches [][]string
	for _, b := range branches {
		bMap, ok := b.(map[string]any)
		if !ok {
			continue
		}
		var branchPresent []string
		if reqList, ok := bMap["required"].([]any); ok {
			for _, r := range reqList {
				if rStr, ok := r.(string); ok {
					if _, present := args[rStr]; present {
						branchPresent = append(branchPresent, rStr)
					}
				}
			}
		}
		if len(branchPresent) > 0 {
			presentBranches = append(presentBranches, branchPresent)
		}
	}
	if len(presentBranches) > 1 {
		var allConflicts []string
		for _, b := range presentBranches {
			allConflicts = append(allConflicts, b...)
		}
		sort.Strings(allConflicts)
		return allConflicts
	}
	return nil
}

func schemaExpectsStructuredJSON(propSchema map[string]any) (bool, string) {
	if propSchema == nil {
		return false, ""
	}
	t, _ := propSchema["type"].(string)
	if t == "array" {
		return true, "array"
	}
	if t == "object" {
		return true, "object"
	}
	for _, comb := range []string{"oneOf", "anyOf"} {
		if list, ok := propSchema[comb].([]any); ok {
			for _, item := range list {
				if branch, ok := item.(map[string]any); ok {
					bt, _ := branch["type"].(string)
					if bt == "array" {
						return true, "array"
					}
					if bt == "object" {
						return true, "object"
					}
				}
			}
		}
	}
	return false, ""
}
