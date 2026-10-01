package findings

import (
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// BuildDigestFromRows maps internal rows into a FindingsDigest wire shape.
func BuildDigestFromRows(rows []Finding, sessionID string) api.FindingsDigest {
	out := make([]api.Finding, 0, len(rows))
	for _, f := range rows {
		out = append(out, ToWire(f))
	}
	return api.FindingsDigest{
		Findings: out,
		Revision: CurrentRevision(sessionID),
	}
}

// ToWire maps an internal finding row to the API shape.
func ToWire(f Finding) api.Finding {
	ts := ""
	if !f.TS.IsZero() {
		ts = f.TS.UTC().Format(time.RFC3339)
	}
	return api.Finding{
		ID:         f.ID,
		Body:       f.Body,
		HasBody:    f.Body != "" || f.HasBody,
		Agent:      f.Agent,
		Summary:    f.Summary,
		Ref:        f.Ref,
		RecordedAt: ts,
	}
}
