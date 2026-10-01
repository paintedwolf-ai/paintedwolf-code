package guidance

import "context"

// RenderPolicyCopy formats the copy already evaluated at the occurrence.
// [OAR-COPY-1] Its templates are not re-evaluated against later host state.
func RenderPolicyCopy(ctx context.Context, code, effect string, copy map[string]string) (string, error) {
	what := copy["what"]
	if what == "" {
		what = copy["title"]
	}
	if what == "" {
		what = copy["cause"]
	}
	if what == "" {
		what = code
	}
	return RenderGuidance(ctx, "reject/_reject", map[string]any{
		"code": code, "effect": effect, "category": "", "what": what,
		"cause": copy["cause"], "why": copy["why"], "fix": copy["fix"], "instead": copy["instead"],
	})
}
