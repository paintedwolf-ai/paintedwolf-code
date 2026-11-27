package pongoplain

import (
	"context"
	"strings"

	"github.com/flosch/pongo2/v6"
)

// LoopBindings returns all forloop.* selector references in source, ignoring
// comments, verbatim blocks, and string literals.
func LoopBindings(source string) []string {
	var out []string
	for _, region := range templateRegion.FindAllString(source, -1) {
		if isOpaqueRegion(region) {
			continue
		}
		for _, tok := range expressionToken.FindAllString(region, -1) {
			if isQuotedLiteral(tok) {
				continue
			}
			selector := strings.Join(strings.Fields(tok), "")
			if strings.HasPrefix(selector, "forloop.") {
				out = append(out, selector)
			}
		}
	}
	return out
}

// ExecuteWithStepHook runs Execute with a deterministic per-step callback for tests.
func ExecuteWithStepHook(
	ctx context.Context,
	tpl *pongo2.Template,
	values map[string]any,
	onStep func(step int),
) (string, error) {
	return executeWithHook(ctx, tpl, values, onStep, false)
}
