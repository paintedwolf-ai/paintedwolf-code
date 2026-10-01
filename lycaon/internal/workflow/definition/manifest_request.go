package definition

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// RequestCadence controls when a workflow accepts a new request.
type RequestCadence string

const (
	RequestCadenceOnce     RequestCadence = "once"
	RequestCadenceEachTurn RequestCadence = "each_turn"
)

// ManifestRequest defines the workflow's user-facing request contract.
type ManifestRequest struct {
	Cadence  RequestCadence
	Question string
	Default  string
}

func parseManifestRequest(workflowID string, raw *requestYAML) (*ManifestRequest, error) {
	if raw == nil {
		return nil, nil
	}
	request := &ManifestRequest{
		Cadence:  RequestCadence(strings.TrimSpace(raw.Cadence)),
		Question: strings.TrimSpace(raw.Question),
		Default:  strings.TrimSpace(raw.Default),
	}
	if request.Cadence == "" {
		request.Cadence = RequestCadenceOnce
	}
	if request.Cadence != RequestCadenceOnce && request.Cadence != RequestCadenceEachTurn {
		return nil, fmt.Errorf("workflow manifest %s: request.cadence must be once or each_turn", workflowID)
	}
	if request.Question == "" {
		return nil, fmt.Errorf("workflow manifest %s: request.question required", workflowID)
	}
	return request, nil
}

func (r *ManifestRequest) Summary() *api.WorkflowRequestSpec {
	if r == nil {
		return nil
	}
	return &api.WorkflowRequestSpec{
		Cadence:  string(r.Cadence),
		Question: strings.TrimSpace(r.Question),
		Default:  strings.TrimSpace(r.Default),
	}
}

func cloneManifestRequest(in *ManifestRequest) *ManifestRequest {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
