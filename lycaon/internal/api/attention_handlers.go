package api

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
)

// handleGetAttention returns attention state across all projects.
func (s *Activity) handleGetAttention(w http.ResponseWriter, r *http.Request) {
	view, err := s.attention.BuildView(r.Context())
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, view)
}
