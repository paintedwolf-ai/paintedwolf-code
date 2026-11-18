package worker

import (
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

type checkpointReviews struct {
	mu    sync.Mutex
	byJob map[string]*checkpoint.PromotionPreview
}

func (r *checkpointReviews) get(jobID string) *checkpoint.PromotionPreview {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byJob[jobID]
}

func (r *checkpointReviews) replace(jobID string, preview *checkpoint.PromotionPreview) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if preview == nil {
		delete(r.byJob, jobID)
		return
	}
	if r.byJob == nil {
		r.byJob = make(map[string]*checkpoint.PromotionPreview)
	}
	r.byJob[jobID] = preview
}
