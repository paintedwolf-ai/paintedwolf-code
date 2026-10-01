package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var schemaCache sync.Map // string(cache key) -> *jsonschema.Schema

// schemaRequiresValidation reports whether ArgsSchema imposes constraints beyond a bare object type.
func schemaRequiresValidation(schema map[string]any) bool {
	if len(schema) == 0 {
		return false
	}
	typ, _ := schema["type"].(string)
	if typ != "object" {
		return true
	}
	for key, value := range schema {
		if key == "type" {
			continue
		}
		if key == "additionalProperties" && value == true {
			continue
		}
		return true
	}
	return false
}

// ValidateToolArgs checks args against a JSON Schema object when the schema is non-trivial.
func ValidateToolArgs(schema map[string]any, args map[string]any) error {
	if !schemaRequiresValidation(schema) {
		return nil
	}
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshal schema: %w", err)
	}
	cacheKey := string(schemaBytes)
	cached, ok := schemaCache.Load(cacheKey)
	var sch *jsonschema.Schema
	if ok {
		sch = cached.(*jsonschema.Schema)
	} else {
		var doc any
		if err := json.Unmarshal(schemaBytes, &doc); err != nil {
			return fmt.Errorf("parse schema: %w", err)
		}
		const schemaURL = "lycaon://tool-args/schema.json"
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(schemaURL, doc); err != nil {
			return fmt.Errorf("compile schema: %w", err)
		}
		sch, err = compiler.Compile(schemaURL)
		if err != nil {
			return fmt.Errorf("compile schema: %w", err)
		}
		schemaCache.Store(cacheKey, sch)
	}
	argsBytes, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(argsBytes))
	if err != nil {
		return fmt.Errorf("parse args: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return err
	}
	return nil
}

type schemaDiagnostic struct {
	RelocatedField      string
	NestedUnder         string
	ExpectedPath        string
	DidYouMean          string
	UnparsedJSON        bool
	Field               string
	ExpectedType        string
	ConflictKeys        []string
	ReplacementArgs     map[string]any
	ReplacementArgsJSON string
}

func levenshtein(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	la := len(ra)
	lb := len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			min := del
			if ins < min {
				min = ins
			}
			if sub < min {
				min = sub
			}
			curr[j] = min
		}
		copy(prev, curr)
	}
	return prev[lb]
}

func extractTopLevelProperties(schema map[string]any) map[string]map[string]any {
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

func extractNestedProperties(topProps map[string]map[string]any) map[string]map[string]map[string]any {
	nested := map[string]map[string]map[string]any{}
	for parent, propSchema := range topProps {
		childProps := extractTopLevelProperties(propSchema)
		if len(childProps) > 0 {
			nested[parent] = childProps
		}
	}
	return nested
}

func cloneArgsMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if subMap, ok := v.(map[string]any); ok {
			out[k] = cloneArgsMap(subMap)
		} else if slice, ok := v.([]any); ok {
			out[k] = append([]any(nil), slice...)
		} else {
			out[k] = v
		}
	}
	return out
}

func checkStringifiedJSON(val any) (any, bool, string) {
	s, ok := val.(string)
	if !ok {
		return nil, false, ""
	}
	s = strings.TrimSpace(s)
	isArray := strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")
	isObj := strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")
	if !isArray && !isObj {
		return nil, false, ""
	}
	expectedType := "object"
	if isArray {
		expectedType = "array"
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err == nil {
		return parsed, true, expectedType
	}
	return nil, false, ""
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

func introspectSchemaError(schema map[string]any, args map[string]any, _ error) schemaDiagnostic {
	var diag schemaDiagnostic
	if schema == nil || args == nil {
		return diag
	}

	topProps := extractTopLevelProperties(schema)
	nestedProps := extractNestedProperties(topProps)

	var topPropKeys []string
	for k := range topProps {
		topPropKeys = append(topPropKeys, k)
	}
	sort.Strings(topPropKeys)

	var parentKeys []string
	for p := range nestedProps {
		parentKeys = append(parentKeys, p)
	}
	sort.Strings(parentKeys)

	var unknownTopKeys []string
	for k := range args {
		if _, ok := topProps[k]; !ok {
			unknownTopKeys = append(unknownTopKeys, k)
		}
	}
	sort.Strings(unknownTopKeys)

	// Relocate fields placed at root that belong in child objects.
	relocatedList := detectRelocatedFields(unknownTopKeys, parentKeys, nestedProps, args)
	if len(relocatedList) > 0 {
		primary := relocatedList[0]
		diag.RelocatedField = primary.field
		diag.NestedUnder = primary.parent
		diag.ExpectedPath = primary.expectedPath
		if primary.isJSON {
			diag.UnparsedJSON = true
			diag.Field = primary.field
			diag.ExpectedType = primary.jsonType
		}
	}

	// Match unexpected properties against declared fields using normalized string distance.
	if len(relocatedList) == 0 && len(unknownTopKeys) > 0 {
		bestMatch, bestUnk := findClosestPropertyMatch(unknownTopKeys, topPropKeys)
		if bestMatch != "" {
			diag.DidYouMean = bestMatch
			diag.Field = bestUnk
		}
	}

	// Decode stringified JSON where array or object types are declared.
	unparsedReplacements, nestedUnparsedReplacements := detectUnparsedJSON(
		topPropKeys, topProps, parentKeys, nestedProps, args, &diag,
	)

	// Detect mutually exclusive properties across oneOf branches.
	diag.ConflictKeys = detectCombinatorConflicts(schema, args)

	// Synthesize and validate repaired arguments.
	repaired, madeChange := synthesizeRepairedArgs(args, relocatedList, unparsedReplacements, nestedUnparsedReplacements)

	if madeChange {
		if err := ValidateToolArgs(schema, repaired); err == nil {
			diag.ReplacementArgs = repaired
			if b, err := json.MarshalIndent(repaired, "", "  "); err == nil {
				diag.ReplacementArgsJSON = string(b)
			}
		}
	}

	return diag
}

func formatArgsInvalidReason(err error, args map[string]any, diag schemaDiagnostic) string {
	if diag.RelocatedField != "" && diag.NestedUnder != "" {
		return fmt.Sprintf("property %q belongs under %q, not at root; received keys: %s",
			diag.RelocatedField, diag.NestedUnder, strings.Join(sortedArgKeys(args), ", "))
	}
	if diag.DidYouMean != "" {
		return fmt.Sprintf("unknown property %q; did you mean %q?; received keys: %s",
			diag.Field, diag.DidYouMean, strings.Join(sortedArgKeys(args), ", "))
	}
	if diag.UnparsedJSON {
		return fmt.Sprintf("property %q received a JSON-encoded string; expected a native %s; received keys: %s",
			diag.Field, diag.ExpectedType, strings.Join(sortedArgKeys(args), ", "))
	}
	if len(diag.ConflictKeys) > 0 {
		return fmt.Sprintf("conflicting properties: provide only one of %s; received keys: %s",
			strings.Join(diag.ConflictKeys, ", "), strings.Join(sortedArgKeys(args), ", "))
	}
	if err == nil {
		return "invalid arguments"
	}
	reason := strings.TrimSpace(err.Error())
	received := sortedArgKeys(args)
	if len(received) == 0 {
		return reason
	}
	return reason + "; received keys: " + strings.Join(received, ", ")
}

func sortedArgKeys(args map[string]any) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for key := range args {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// mutationRecoveryTool suggests write when a span or structural edit fails
// schema validation and write is addressable this turn.
func mutationRecoveryTool(tool string, addressable []string) string {
	switch strings.TrimSpace(tool) {
	case "edit", "replace_lines", "code_rewrite":
	default:
		return ""
	}
	for _, name := range addressable {
		if name == "write" {
			return "write"
		}
	}
	return ""
}

// RejectInvalidArguments marks an observed argument check independently of its diagnostic.
func RejectInvalidArguments(code string, data map[string]any) *ToolReject {
	return &ToolReject{Code: code, Data: data, ArgumentValidation: true}
}

// ValidateCallArguments checks transport integrity and argument shape before policy reads them.
func ValidateCallArguments(qualifiedName string, args, schema map[string]any, tc ToolContext) *ToolReject {
	if tc.ArgsTruncated {
		return RejectInvalidArguments("TOOL_ARGS_TRUNCATED", map[string]any{"tool": qualifiedName})
	}
	if tc.ArgsMalformed {
		return RejectInvalidArguments("TOOL_ARGS_MALFORMED", map[string]any{"tool": qualifiedName})
	}
	if _, err := json.Marshal(args); err != nil {
		return RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
			"tool": qualifiedName, "reason": "arguments cannot be encoded as JSON",
		})
	}
	if schema != nil {
		if err := ValidateToolArgs(schema, args); err != nil {
			var invalid *jsonschema.ValidationError
			if !errors.As(err, &invalid) {
				return &ToolReject{Code: ToolOwnerFailedCode, Data: map[string]any{"tool": qualifiedName, "reason": err.Error()}}
			}
			diag := introspectSchemaError(schema, args, err)
			data := map[string]any{
				"reason": formatArgsInvalidReason(err, args, diag),
				"tool":   qualifiedName,
			}
			if diag.RelocatedField != "" {
				data["relocated_field"] = diag.RelocatedField
				data["nested_under"] = diag.NestedUnder
				data["expected_path"] = diag.ExpectedPath
			}
			if diag.DidYouMean != "" {
				data["did_you_mean"] = diag.DidYouMean
				data["field"] = diag.Field
			}
			if diag.UnparsedJSON {
				data["unparsed_json"] = true
				data["field"] = diag.Field
				data["expected_type"] = diag.ExpectedType
			}
			if len(diag.ConflictKeys) > 0 {
				data["conflict_keys"] = diag.ConflictKeys
			}
			if diag.ReplacementArgs != nil {
				data["replacement_args"] = diag.ReplacementArgs
				data["replacement_args_json"] = diag.ReplacementArgsJSON
			}
			if sibling := mutationRecoveryTool(qualifiedName, tc.TurnToolPlan.AddressableNames()); sibling != "" {
				data["suggested_tool"] = sibling
			}
			return RejectInvalidArguments("TOOL_ARGS_INVALID", data)
		}
	}
	return nil
}

type relocatedInfo struct {
	field        string
	parent       string
	expectedPath string
	parsedJSON   any
	isJSON       bool
	jsonType     string
}

func detectRelocatedFields(unknownTopKeys, parentKeys []string, nestedProps map[string]map[string]map[string]any, args map[string]any) []relocatedInfo {
	var relocatedList []relocatedInfo
	for _, unk := range unknownTopKeys {
		for _, parent := range parentKeys {
			children := nestedProps[parent]
			childSchema, ok := children[unk]
			if !ok {
				continue
			}
			info := relocatedInfo{
				field:        unk,
				parent:       parent,
				expectedPath: parent + "." + unk,
			}
			if expectsJSON, _ := schemaExpectsStructuredJSON(childSchema); expectsJSON {
				if parsed, isJSON, expType := checkStringifiedJSON(args[unk]); isJSON {
					info.parsedJSON = parsed
					info.isJSON = true
					info.jsonType = expType
				}
			}
			relocatedList = append(relocatedList, info)
			break
		}
	}
	return relocatedList
}

func findClosestPropertyMatch(unknownTopKeys, topPropKeys []string) (string, string) {
	bestMatch := ""
	bestUnk := ""
	bestSim := 0.0
	minDist := 999
	for _, unk := range unknownTopKeys {
		unkLower := strings.ToLower(unk)
		for _, declared := range topPropKeys {
			declLower := strings.ToLower(declared)
			if unkLower == declLower {
				continue
			}
			d := levenshtein(unkLower, declLower)
			maxLen := len(unkLower)
			if len(declLower) > maxLen {
				maxLen = len(declLower)
			}
			minLen := len(unkLower)
			if len(declLower) < minLen {
				minLen = len(declLower)
			}
			if maxLen == 0 {
				continue
			}
			sim := 1.0 - (float64(d) / float64(maxLen))
			isMatch := false
			if sim >= 0.70 {
				isMatch = true
			} else if d <= 2 && minLen >= 4 {
				isMatch = true
			} else if (strings.HasPrefix(declLower, unkLower) || strings.HasPrefix(unkLower, declLower)) && minLen >= 3 && sim >= 0.65 {
				isMatch = true
			}

			if isMatch && (sim > bestSim || (sim == bestSim && d < minDist)) {
				bestSim = sim
				minDist = d
				bestMatch = declared
				bestUnk = unk
			}
		}
	}
	return bestMatch, bestUnk
}

func detectUnparsedJSON(
	topPropKeys []string,
	topProps map[string]map[string]any,
	parentKeys []string,
	nestedProps map[string]map[string]map[string]any,
	args map[string]any,
	diag *schemaDiagnostic,
) (map[string]any, map[string]map[string]any) {
	unparsedReplacements := map[string]any{}
	for _, k := range topPropKeys {
		v, present := args[k]
		if !present {
			continue
		}
		propSchema := topProps[k]
		expectsJSON, expectedType := schemaExpectsStructuredJSON(propSchema)
		if !expectsJSON {
			continue
		}
		if parsed, isJSON, _ := checkStringifiedJSON(v); isJSON {
			if !diag.UnparsedJSON {
				diag.UnparsedJSON = true
				diag.Field = k
				diag.ExpectedType = expectedType
			}
			unparsedReplacements[k] = parsed
		}
	}

	nestedUnparsedReplacements := map[string]map[string]any{}
	for _, parent := range parentKeys {
		parentVal, ok := args[parent].(map[string]any)
		if !ok {
			continue
		}
		children := nestedProps[parent]
		var childKeys []string
		for ck := range children {
			childKeys = append(childKeys, ck)
		}
		sort.Strings(childKeys)
		for _, ck := range childKeys {
			cv, present := parentVal[ck]
			if !present {
				continue
			}
			childSchema := children[ck]
			expectsJSON, expectedType := schemaExpectsStructuredJSON(childSchema)
			if !expectsJSON {
				continue
			}
			if parsed, isJSON, _ := checkStringifiedJSON(cv); isJSON {
				if !diag.UnparsedJSON {
					diag.UnparsedJSON = true
					diag.Field = parent + "." + ck
					diag.ExpectedType = expectedType
				}
				if nestedUnparsedReplacements[parent] == nil {
					nestedUnparsedReplacements[parent] = map[string]any{}
				}
				nestedUnparsedReplacements[parent][ck] = parsed
			}
		}
	}
	return unparsedReplacements, nestedUnparsedReplacements
}

func synthesizeRepairedArgs(args map[string]any, relocatedList []relocatedInfo, unparsedReplacements map[string]any, nestedUnparsedReplacements map[string]map[string]any) (map[string]any, bool) {
	repaired := cloneArgsMap(args)
	madeChange := false

	for _, rel := range relocatedList {
		val := repaired[rel.field]
		if rel.parsedJSON != nil {
			val = rel.parsedJSON
		}
		delete(repaired, rel.field)
		var parentMap map[string]any
		if existing, ok := repaired[rel.parent].(map[string]any); ok {
			parentMap = cloneArgsMap(existing)
		} else {
			parentMap = map[string]any{}
		}
		parentMap[rel.field] = val
		repaired[rel.parent] = parentMap
		madeChange = true
	}

	for k, parsed := range unparsedReplacements {
		repaired[k] = parsed
		madeChange = true
	}

	for parent, fields := range nestedUnparsedReplacements {
		var parentMap map[string]any
		if existing, ok := repaired[parent].(map[string]any); ok {
			parentMap = cloneArgsMap(existing)
		} else {
			parentMap = map[string]any{}
		}
		for ck, parsed := range fields {
			parentMap[ck] = parsed
			madeChange = true
		}
		repaired[parent] = parentMap
	}

	return repaired, madeChange
}
