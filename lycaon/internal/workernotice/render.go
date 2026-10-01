package workernotice

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// Renderer renders worker execute failures from the user-notice catalog.
type Renderer struct {
	catalog *usernotice.Catalog
}

// NewRenderer wires catalog-backed worker failure copy.
func NewRenderer(catalog *usernotice.Catalog) *Renderer {
	return &Renderer{catalog: catalog}
}

// RenderExecuteFailure implements worker.ExecuteFailureRenderer.
func (r *Renderer) RenderExecuteFailure(err error) api.WorkerFailure {
	code := worker.ExecuteFailureCode(err)
	out := api.WorkerFailure{Code: code}
	if r == nil || r.catalog == nil {
		return out
	}
	var context map[string]any
	var capacityErr *workspace.CapacityError
	if errors.As(err, &capacityErr) {
		context = map[string]any{
			"reason": "capacity_preflight", "required": humanBytes(capacityErr.Required), "available": humanBytes(capacityErr.Available),
		}
	}
	copy := r.catalog.RenderWire(code, context)
	out.Title = copy.Title
	out.Message = copy.Message
	out.SuggestedAction = copy.SuggestedAction
	for _, a := range copy.Actions {
		out.Actions = append(out.Actions, api.NoticeAction(a))
	}
	if placement, ok := r.catalog.Placement(code, nil); ok {
		out.Tier = api.NoticeTier(placement.Tier)
		out.Scope = api.NoticeScope(placement.Scope)
		out.Resolution = placement.ID
	}
	return out
}

func humanBytes(bytes uint64) string {
	const (
		gib = 1024 * 1024 * 1024
		mib = 1024 * 1024
	)
	if bytes >= gib {
		return fmt.Sprintf("%.1f GiB", float64(bytes)/gib)
	}
	if bytes >= mib {
		return fmt.Sprintf("%.1f MiB", float64(bytes)/mib)
	}
	return fmt.Sprintf("%d bytes", bytes)
}
