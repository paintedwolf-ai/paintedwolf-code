package oar

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcore"
)

func copyInterpolatableType(typ string) bool {
	switch typ {
	case "string", "bool", "int", "double", "list<string>":
		return true
	}
	return false
}

func DeclaresCopyFact(name string) bool {
	_, ok := publishedFacts[name]
	return ok
}

func copyFields(c Copy) []struct{ name, text string } {
	return []struct{ name, text string }{
		{"title", c.Title},
		{"what", c.What},
		{"cause", c.Cause},
		{"why", c.Why},
		{"fix", c.Fix},
		{"instead", c.Instead},
	}
}

// ValidateRuleCopy rejects illegal constructs, unknown names, and
// non-interpolatable types ([OAR-COPY-2]–[OAR-COPY-4]).
func ValidateRuleCopy(r *Rule) error {
	if r == nil {
		return nil
	}
	for _, field := range copyFields(r.Copy) {
		if strings.TrimSpace(field.text) == "" {
			continue
		}
		bindings, err := oarcore.ParseCopy(field.text)
		if err != nil {
			return fmt.Errorf("[OAR-COPY-3] rule %s copy.%s carries construct %s", r.ID, field.name, err.Error())
		}
		for _, b := range bindings {
			if _, ok := publishedFacts[b.Name]; !ok {
				return fmt.Errorf("[OAR-COPY-2] rule %s copy.%s names %s, which this engine does not declare", r.ID, field.name, b.Name)
			}
			if b.Interpolate && !copyInterpolatableType(declaredFactTypes[b.Name]) {
				return fmt.Errorf("[OAR-COPY-4] rule %s copy.%s interpolates %s, whose type is %s", r.ID, field.name, b.Name, declaredFactTypes[b.Name])
			}
		}
	}
	return nil
}

// RenderRuleCopy renders every authored copy member against occurrence facts.
func RenderRuleCopy(r *Rule, facts map[string]any) map[string]string {
	if r == nil {
		return map[string]string{}
	}
	if r.document != nil {
		out, err := r.document.RenderCopy(facts)
		if err != nil {
			return map[string]string{}
		} // [OAR-COPY-11] Error blocks retain identity without invalid copy.
		return out
	}
	out := map[string]string{}
	for _, field := range copyFields(r.Copy) {
		if strings.TrimSpace(field.text) == "" {
			continue
		}
		out[field.name] = oarcore.RenderCopy(field.text, facts)
	}
	return out
}

func copyRefs(c Copy) []string {
	var names []string
	seen := map[string]bool{}
	for _, field := range copyFields(c) {
		if strings.TrimSpace(field.text) == "" {
			continue
		}
		bindings, err := oarcore.ParseCopy(field.text)
		if err != nil {
			continue
		}
		for _, b := range bindings {
			if seen[b.Name] {
				continue
			}
			seen[b.Name] = true
			names = append(names, b.Name)
		}
	}
	return names
}
