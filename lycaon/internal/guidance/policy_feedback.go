package guidance

import (
	"context"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/pkg/api"
	"maps"
)

// PolicyFeedback is one frozen rule result. Its copy and machine facts travel together.
type PolicyFeedback struct {
	Anchor string `json:"anchor"`
	Rule   string `json:"rule"`
	Effect string `json:"effect"`
	api.ToolFeedback
	Copy map[string]string `json:"-"`
}

func (f PolicyFeedback) Clone() PolicyFeedback {
	f.Details = jsonvalue.CloneMap(f.Details)
	f.Copy = maps.Clone(f.Copy)
	if f.Subject != nil {
		subject := *f.Subject
		f.Subject = &subject
	}
	return f
}

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
