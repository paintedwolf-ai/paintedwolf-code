package guidance

import (
	"context"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/pkg/api"
)

// PolicyFeedback is one frozen rule result. Its copy and machine facts travel together.
type PolicyFeedback struct {
	Anchor string `json:"anchor"`
	Rule   string `json:"rule"`
	Effect string `json:"effect"`
	Retain string `json:"retain,omitempty"`
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
func RenderPolicyCopy(ctx context.Context, code, effect string, copy map[string]string, details map[string]any) (string, error) {
	what := strings.TrimSpace(copy["what"])
	if what == "" {
		what = strings.TrimSpace(copy["title"])
	}
	if what == "" {
		what = strings.TrimSpace(copy["cause"])
	}
	if what == "" {
		what = code
	}
	return RenderGuidance(ctx, "reject/_reject", map[string]any{
		"code": code, "effect": effect, "category": "", "what": what,
		"execution": commandRecoveryView(details),
		"cause":     strings.TrimSpace(copy["cause"]), "why": strings.TrimSpace(copy["why"]), "fix": strings.TrimSpace(copy["fix"]), "instead": strings.TrimSpace(copy["instead"]),
	})
}
