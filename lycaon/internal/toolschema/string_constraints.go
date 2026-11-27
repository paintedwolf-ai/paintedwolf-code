package toolschema

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/scopedstore"
)

// SchemaAdmitsStringValue reports whether projected admits fieldPath and string value,
// enforcing intersecting enum/const/combinator constraints and admitting unconstrained strings.
func SchemaAdmitsStringValue(projected map[string]any, fieldPath, value string) bool {
	fieldPath = strings.TrimSpace(fieldPath)
	if fieldPath == "" || projected == nil {
		return false
	}
	declared, _, admitted, _ := evalPathStringConstraint(projected, "", fieldPath, value, 0)
	return declared && admitted
}

// SchemaAcceptsEnumValue reports whether projected declares fieldPath with an
// enum or const constraint that admits value under all intersecting constraints.
func SchemaAcceptsEnumValue(projected map[string]any, fieldPath, value string) bool {
	fieldPath = strings.TrimSpace(fieldPath)
	if fieldPath == "" || value == "" || projected == nil {
		return false
	}
	declared, _, admitted, explicit := evalPathStringConstraint(projected, "", fieldPath, value, 0)
	return declared && admitted && explicit
}

func evalPathStringConstraint(node map[string]any, prefix, target, value string, depth int) (bool, bool, bool, bool) {
	if node == nil || depth > argFieldDepthMax {
		return false, true, false, false
	}
	for _, key := range []string{"not", "if", "then", "else", "dependentSchemas"} {
		if _, exists := node[key]; exists {
			return false, true, false, false
		}
	}
	declared, constrains, explicit := false, false, false
	if pDecl, pConst, pAdmits, pExplicit := evalObjectOrItemsStep(node, prefix, target, value, depth); pConst {
		constrains = true
		if pDecl {
			declared = true
		}
		if !pAdmits {
			return declared, true, false, false
		}
		if pExplicit {
			explicit = true
		}
	} else if pDecl {
		declared = true
	}

	if aDecl, aConst, aAdmits, aExplicit := evalPathAllOf(node, prefix, target, value, depth); aConst || aDecl {
		if aDecl {
			declared = true
		}
		if aConst {
			constrains = true
			if !aAdmits {
				return declared, true, false, false
			}
		}
		if aExplicit {
			explicit = true
		}
	}

	if anyDecl, anyConst, anyAdmits, anyExplicit := evalPathAnyOf(node, prefix, target, value, depth); anyConst || anyDecl {
		if anyDecl {
			declared = true
		}
		if anyConst {
			constrains = true
			if !anyAdmits {
				return declared, true, false, false
			}
		}
		if anyExplicit {
			explicit = true
		}
	}

	if oDecl, oConst, oAdmits, oExplicit := evalPathOneOf(node, prefix, target, value, depth); oConst || oDecl {
		if oDecl {
			declared = true
		}
		if oConst {
			constrains = true
			if !oAdmits {
				return declared, true, false, false
			}
		}
		if oExplicit {
			explicit = true
		}
	}
	return declared, constrains, true, explicit
}

func evalObjectOrItemsStep(node map[string]any, prefix, target, value string, depth int) (bool, bool, bool, bool) {
	if props, hasProps := node["properties"].(map[string]any); hasProps {
		for name, raw := range props {
			if strings.TrimSpace(name) == "" {
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if path != target && !strings.HasPrefix(target, path+".") {
				continue
			}
			child, isMap := raw.(map[string]any)
			if !isMap || child == nil {
				return false, true, false, false
			}
			if path == target {
				admits, explicit := evalValueStringConstraint(child, value, depth+1)
				return true, true, admits, explicit
			}
			cDecl, cConst, cAdmits, cExplicit := evalPathStringConstraint(child, path, target, value, depth+1)
			return cDecl, true, !cConst || cAdmits, cExplicit
		}
		if addl, isBool := node["additionalProperties"].(bool); isBool && !addl {
			return false, true, false, false
		}
		return false, false, true, false
	}
	if items, hasItems := node["items"].(map[string]any); hasItems {
		return evalPathStringConstraint(items, prefix, target, value, depth+1)
	}
	if addl, isBool := node["additionalProperties"].(bool); isBool && !addl {
		return false, true, false, false
	}
	return false, false, true, false
}

func evalPathAllOf(node map[string]any, prefix, target, value string, depth int) (bool, bool, bool, bool) {
	rawAll, hasAll := node["allOf"]
	if !hasAll {
		return false, false, true, false
	}
	branches, ok := rawAll.([]any)
	if !ok || len(branches) == 0 {
		return false, true, false, false
	}
	declared, constrains, explicit := false, false, false
	for _, raw := range branches {
		branch, isMap := raw.(map[string]any)
		if !isMap || branch == nil {
			return false, true, false, false
		}
		bDecl, bConst, bAdmits, bExplicit := evalPathStringConstraint(branch, prefix, target, value, depth+1)
		if bDecl {
			declared = true
		}
		if bConst {
			constrains = true
			if !bAdmits {
				return declared, true, false, false
			}
		}
		if bExplicit {
			explicit = true
		}
	}
	return declared, constrains, true, explicit
}

func evalPathAnyOf(node map[string]any, prefix, target, value string, depth int) (bool, bool, bool, bool) {
	rawAny, hasAny := node["anyOf"]
	if !hasAny {
		return false, false, true, false
	}
	branches, ok := rawAny.([]any)
	if !ok || len(branches) == 0 {
		return false, true, false, false
	}
	matched, anyDecl, anyConst, allMatchedExplicit := 0, false, false, true
	for _, raw := range branches {
		branch, isMap := raw.(map[string]any)
		if !isMap || branch == nil {
			return false, true, false, false
		}
		bDecl, bConst, bAdmits, bExplicit := evalPathStringConstraint(branch, prefix, target, value, depth+1)
		if bConst {
			anyConst = true
		}
		if !bConst || bAdmits {
			matched++
			if bDecl {
				anyDecl = true
			}
			if !bExplicit {
				allMatchedExplicit = false
			}
		}
	}
	if matched == 0 {
		return false, true, false, false
	}
	return anyDecl, anyConst, true, allMatchedExplicit
}

func evalPathOneOf(node map[string]any, prefix, target, value string, depth int) (bool, bool, bool, bool) {
	rawOne, hasOne := node["oneOf"]
	if !hasOne {
		return false, false, true, false
	}
	branches, ok := rawOne.([]any)
	if !ok || len(branches) == 0 {
		return false, true, false, false
	}
	matched, anyConst, soleDecl, soleExplicit, hasMixed := 0, false, false, false, false
	for _, raw := range branches {
		branch, isMap := raw.(map[string]any)
		if !isMap || branch == nil {
			return false, true, false, false
		}
		bDecl, bConst, bAdmits, bExplicit := evalPathStringConstraint(branch, prefix, target, value, depth+1)
		if bConst {
			anyConst = true
			if branchConstrainsOtherFields(branch, prefix, target) {
				hasMixed = true
			}
		}
		if !bConst || bAdmits {
			matched++
			if bDecl {
				soleDecl = true
			}
			soleExplicit = bExplicit
		}
	}
	if matched == 0 {
		return false, true, false, false
	}
	if !anyConst {
		return false, false, true, false
	}
	if matched != 1 || hasMixed {
		return soleDecl, true, false, false
	}
	return soleDecl, true, true, soleExplicit
}

func branchConstrainsOtherFields(branch map[string]any, prefix, target string) bool {
	if _, hasReq := branch["required"]; hasReq {
		return true
	}
	if props, ok := branch["properties"].(map[string]any); ok {
		for name := range props {
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if path != target && !strings.HasPrefix(target, path+".") {
				return true
			}
		}
	}
	return false
}

func evalValueStringConstraint(node map[string]any, value string, depth int) (bool, bool) {
	if node == nil || depth > argFieldDepthMax {
		return false, false
	}
	for _, key := range []string{"not", "if", "then", "else", "dependentSchemas"} {
		if _, exists := node[key]; exists {
			return false, false
		}
	}
	explicit := false
	if items, hasItems := node["items"].(map[string]any); hasItems {
		if rawType, hasType := node["type"]; hasType && !schemaTypeAdmitsKind(rawType, "array") {
			return false, false
		}
		if maxItems, hasMax := schemaNonNegInt(node, "maxItems"); hasMax && maxItems < 1 {
			return false, false
		}
		itemAdmits, itemExplicit := evalValueStringConstraint(items, value, depth+1)
		if !itemAdmits {
			return false, false
		}
		explicit = itemExplicit
	} else {
		if !scalarStringBoundsAdmit(node, value) {
			return false, false
		}
		if rawEnum, hasEnum := node["enum"]; hasEnum {
			if !stringSliceContains(rawEnum, value) {
				return false, false
			}
			explicit = true
		}
		if rawConst, hasConst := node["const"]; hasConst {
			s, ok := rawConst.(string)
			if !ok || s != value {
				return false, false
			}
			explicit = true
		}
	}
	admitsComb, combExplicit := evalValueCombinators(node, value, depth)
	if !admitsComb {
		return false, false
	}
	return true, explicit || combExplicit
}

func scalarStringBoundsAdmit(node map[string]any, value string) bool {
	if rawType, hasType := node["type"]; hasType && !schemaTypeAdmitsString(rawType) {
		return false
	}
	if minLen, hasMin := schemaNonNegInt(node, "minLength"); hasMin {
		if minLen < 0 || utf8.RuneCountInString(value) < minLen {
			return false
		}
	}
	if maxLen, hasMax := schemaNonNegInt(node, "maxLength"); hasMax {
		if maxLen < 0 || utf8.RuneCountInString(value) > maxLen {
			return false
		}
	}
	if rawPat, hasPat := node["pattern"]; hasPat {
		pat, ok := rawPat.(string)
		if !ok {
			return false
		}
		if re := compiledPattern(pat); re == nil || !re.MatchString(value) {
			return false
		}
	}
	return true
}

// patternCache holds compiled schema patterns; a nil entry records a pattern RE2 cannot compile.
var patternCache = scopedstore.New[*regexp.Regexp](scopedstore.DefaultEntries)

// compiledPattern returns nil for a pattern RE2 cannot compile: an unevaluable
// constraint admits no value, so the host never offers a value it cannot check.
func compiledPattern(pattern string) *regexp.Regexp {
	if re, ok := patternCache.Load(pattern); ok {
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		patternCache.Store(pattern, nil)
		return nil
	}
	patternCache.Store(pattern, re)
	return re
}

func stringSliceContains(raw any, value string) bool {
	values, ok := copySchemaList(raw)
	if !ok {
		return false
	}
	for _, v := range values {
		if s, isStr := v.(string); isStr && s == value {
			return true
		}
	}
	return false
}

func evalValueCombinators(node map[string]any, value string, depth int) (bool, bool) {
	explicit := false
	if rawAll, hasAll := node["allOf"]; hasAll {
		branches, ok := rawAll.([]any)
		if !ok || len(branches) == 0 {
			return false, false
		}
		for _, raw := range branches {
			branch, isMap := raw.(map[string]any)
			if !isMap || branch == nil {
				return false, false
			}
			bAdmits, bExplicit := evalValueStringConstraint(branch, value, depth+1)
			if !bAdmits {
				return false, false
			}
			if bExplicit {
				explicit = true
			}
		}
	}
	if rawAny, hasAny := node["anyOf"]; hasAny {
		branches, ok := rawAny.([]any)
		if !ok || len(branches) == 0 {
			return false, false
		}
		matched, allMatchedExplicit := 0, true
		for _, raw := range branches {
			branch, isMap := raw.(map[string]any)
			if !isMap || branch == nil {
				return false, false
			}
			bAdmits, bExplicit := evalValueStringConstraint(branch, value, depth+1)
			if bAdmits {
				matched++
				if !bExplicit {
					allMatchedExplicit = false
				}
			}
		}
		if matched == 0 {
			return false, false
		}
		if allMatchedExplicit {
			explicit = true
		}
	}
	if rawOne, hasOne := node["oneOf"]; hasOne {
		branches, ok := rawOne.([]any)
		if !ok || len(branches) == 0 {
			return false, false
		}
		matched, soleExplicit := 0, false
		for _, raw := range branches {
			branch, isMap := raw.(map[string]any)
			if !isMap || branch == nil {
				return false, false
			}
			bAdmits, bExplicit := evalValueStringConstraint(branch, value, depth+1)
			if _, hasFmt := branch["format"]; hasFmt && !bExplicit {
				return false, false
			}
			if bAdmits {
				matched++
				soleExplicit = bExplicit
			}
		}
		if matched != 1 {
			return false, false
		}
		if soleExplicit {
			explicit = true
		}
	}
	return true, explicit
}

func schemaTypeAdmitsString(raw any) bool {
	return schemaTypeAdmitsKind(raw, "string")
}

func schemaTypeAdmitsKind(raw any, kind string) bool {
	if s, ok := raw.(string); ok {
		return s == kind
	}
	if list, ok := copySchemaList(raw); ok {
		for _, v := range list {
			if s, isStr := v.(string); isStr && s == kind {
				return true
			}
		}
	}
	return false
}

func schemaNonNegInt(node map[string]any, key string) (int, bool) {
	raw, exists := node[key]
	if !exists {
		return 0, false
	}
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if float64(int(v)) != v {
			return -1, true
		}
		return int(v), true
	default:
		return -1, true
	}
}
