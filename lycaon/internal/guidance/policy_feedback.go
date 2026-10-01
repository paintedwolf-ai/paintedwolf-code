package guidance

import (
	"maps"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/pkg/api"
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
