package researchadmin

import (
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/webindex"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// GetIndex reports index state and warming activity.
func (s *Handler) GetIndex(w http.ResponseWriter, r *http.Request) {
	status, err := s.webResearchIndexStatus(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, status)
}

func (s *Handler) webResearchIndexStatus(r *http.Request) (wire.WebResearchIndexStatus, error) {
	status := wire.WebResearchIndexStatus{Warming: s.Runtime.Config.WarmingEnabled(), Activity: []wire.WebResearchWarmActivity{}}
	if s.Index == nil {
		return status, nil
	}
	stats, err := s.Index.Stats(r.Context())
	if err != nil {
		return status, err
	}
	status.Available = true
	status.Docs = stats.Docs
	status.Hosts = stats.Hosts
	status.Bytes = stats.Bytes
	status.Verified = stats.Verified
	status.Warmed = stats.Warmed
	status.WarmHits = stats.WarmHits
	status.Health = wire.WebResearchIndexHealth{
		WritesApplied:  stats.WritesApplied,
		WritesDropped:  stats.WritesDropped,
		WritesFailed:   stats.WritesFailed,
		QueueHighWater: stats.QueueHighWater,
		LastEvictMs:    stats.LastEvictMs,
		LastEvictRows:  stats.LastEvictRows,
		LastEvictBytes: stats.LastEvictBytes,
		LastSearchMs:   stats.LastSearchMs,
		MaxSearchMs:    stats.MaxSearchMs,
	}
	acts, err := s.Index.RecentActivity(r.Context(), webindex.MaxActivityRows)
	if err != nil {
		return status, err
	}
	for _, a := range acts {
		status.Activity = append(status.Activity, wire.WebResearchWarmActivity{
			At: a.At.UTC().Format(time.RFC3339), Trigger: a.Trigger, Tier: a.Tier,
			Topic: a.Topic, Hosts: a.Hosts, Pages: a.Pages,
			DurationMs: a.DurationMs, SkipReason: a.SkipReason,
			SessionID: a.SessionID,
		})
	}
	return status, nil
}
