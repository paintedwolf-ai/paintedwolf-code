package sessionadmin

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// WithSessionPanicRecovery repairs the affected session before writing a panic response.
// Recovered panics stop here, avoiding a second response from router middleware.
func (s *Handler) WithSessionPanicRecovery(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func(requestCtx context.Context) {
			rec := recover()
			if rec == nil {
				return
			}
			sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
			if sessionID != "" {
				recoverCtx := context.WithoutCancel(requestCtx)
				if err := s.Sessions.Stops.Recovery.RecoverSession(recoverCtx, sessionID); err != nil {
					s.responses.Logger.ErrorContext(recoverCtx, "scoped session recovery after panic failed",
						"session_id", sessionID, "error", err)
				}
			}
			s.responses.InternalError(w, r, fmt.Errorf("panic: %v", rec))
		}(r.Context())
		next(w, r)
	}
}
