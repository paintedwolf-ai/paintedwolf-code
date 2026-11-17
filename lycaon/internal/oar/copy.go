package oar

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oarcore"
)

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
		{"message", c.Message},
	}
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
		} // Error blocks retain identity without invalid copy.
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
