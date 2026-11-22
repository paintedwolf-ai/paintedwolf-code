package toolvocabtest

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/toolvocab"
)

func parseStandaloneSchemaClaim(c *toolvocab.Catalog, idx *PrunableSchemaIndex, sent sentenceContext, raw string) (path string, val any, hasVal bool, ok bool) {
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		m, ell, _, parsed := parseObjectLiteral(raw)
		if !parsed || ell {
			return "", nil, false, false
		}
		if kv, exists := m["kind"]; exists && len(m) <= 2 {
			if lit, isLit := concreteStringOrIdent(kv); isLit && idx != nil {
				for _, ek := range idx.AllPrunableEnums() {
					if strings.HasSuffix(ek.Path, ".kind") {
						if base, hasBase := idx.BaseSchema(ek.Tool); hasBase && slices.Contains(SchemaEnums(base)[ek.Path], lit) {
							return ek.Path, kv, true, true
						}
					}
				}
			}
		}
		return "", nil, false, false
	}
	if k, vStr, hasSep := splitKeyValueTopLevel(raw); hasSep && identPathRe.MatchString(k) {
		norm := normalizeSchemaPath(k)
		if len(idx.AllOwners(norm)) == 0 {
			return "", nil, false, false
		}
		parsedVal, ell, _ := parseValueExpr(vStr)
		if ell {
			return "", nil, false, false
		}
		explicitTool := explicitPrepositionTool(sent.sentence, raw)
		if explicitTool != "" && (c == nil || !c.KnownName(explicitTool)) {
			explicitTool = ""
		}
		if explicitTool != "" || len(idx.PrunableOwners(norm)) > 0 || isPrunableEnumValue(idx, norm, parsedVal) {
			return norm, parsedVal, true, true
		}
		return "", nil, false, false
	}
	if !identPathRe.MatchString(raw) {
		return "", nil, false, false
	}
	norm := normalizeSchemaPath(raw)
	explicitTool := explicitPrepositionTool(sent.sentence, raw)
	if explicitTool != "" && (c == nil || !c.KnownName(explicitTool)) {
		explicitTool = ""
	}
	if (explicitTool == "" || explicitTool == norm) && c != nil && c.KnownName(norm) {
		return "", nil, false, false
	}
	if explicitTool == "command" && isInCommandProgramContext(sent.sentence, raw) && isKnownProgramOrCLI(norm) && !strings.Contains(norm, ".") {
		return "", nil, false, false
	}
	if len(idx.AllOwners(norm)) == 0 {
		if explicitTool == "" {
			return "", nil, false, false
		}
		if enumPath, ok := singleEnumPathForLiteral(idx, explicitTool, norm); ok {
			return enumPath, norm, true, true
		}
		if (c != nil && c.KnownName(norm)) || isKnownProgramOrCLI(norm) {
			return "", nil, false, false
		}
		return norm, nil, false, true
	}
	if explicitTool != "" {
		return norm, nil, false, true
	}
	if len(idx.PrunableOwners(norm)) == 0 {
		return "", nil, false, false
	}
	if !strings.Contains(norm, ".") && !strings.Contains(norm, "_") {
		if len(intersectSorted(sent.sentenceTools, idx.PrunableOwners(norm))) == 0 {
			return "", nil, false, false
		}
	}
	return norm, nil, false, true
}

var inCommandPrepRe = regexp.MustCompile(`\bin\s+(?:\*\*)?` + "`?" + `command` + "`?")

func isInCommandProgramContext(sentence, raw string) bool {
	idx := strings.Index(sentence, "`"+raw+"`")
	if idx < 0 {
		return false
	}
	return inCommandPrepRe.MatchString(sentence[idx+len(raw)+2:])
}

func isKnownProgramOrCLI(norm string) bool {
	if norm == "external" {
		return true
	}
	v, err := StockBacktickVocab()
	if err != nil || v == nil {
		return false
	}
	return v.IsKnownProgramOrCLI(norm)
}

func singleEnumPathForLiteral(idx *PrunableSchemaIndex, tool, lit string) (string, bool) {
	if idx == nil || tool == "" || lit == "" {
		return "", false
	}
	base, ok := idx.BaseSchema(tool)
	if !ok {
		return "", false
	}
	var matched string
	for p, vals := range SchemaEnums(base) {
		if slices.Contains(vals, lit) {
			if matched != "" && matched != p {
				return "", false
			}
			matched = p
		}
	}
	return matched, matched != ""
}

func resolveAttributedTools(c *toolvocab.Catalog, idx *PrunableSchemaIndex, sent sentenceContext, rawSpan, path string, hasVal bool, val any) []string {
	if explicit := explicitPrepositionTool(sent.sentence, rawSpan); explicit != "" && c != nil && c.KnownName(explicit) {
		return []string{explicit}
	}
	allOwners := idx.AllOwners(path)
	compat := compatibleBaseOwners(idx, path, hasVal, val)
	if inSentAll := intersectSorted(sent.sentenceTools, allOwners); len(inSentAll) > 0 {
		if inSentCompat := intersectSorted(inSentAll, compat); len(inSentCompat) > 0 {
			return inSentCompat
		}
		return inSentAll
	}
	if len(compat) == 0 {
		return allOwners
	}
	if len(compat) == 1 {
		return compat
	}
	if inSec := intersectSorted(sent.sectionTools, compat); len(inSec) > 0 {
		return inSec
	}
	return compat
}

func anyAttributedToolAccepts(
	attributed []string,
	path string,
	hasVal bool,
	val any,
	offered map[string]bool,
	schema func(tool string) (map[string]any, bool),
) bool {
	for _, t := range attributed {
		if offered != nil && !offered[t] {
			continue
		}
		s, ok := schema(t)
		if !ok {
			continue
		}
		if !schemaAcceptsPath(s, path) {
			if hasVal || strings.Contains(path, ".") || !slices.ContainsFunc(SchemaPropertyPaths(s), func(p string) bool { return strings.HasSuffix(p, "."+path) }) {
				continue
			}
		}
		if hasVal && len(validateValueAtPath(t, s, path, val, 0)) > 0 {
			continue
		}
		return true
	}
	return false
}
