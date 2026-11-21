package promptadmin

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Submission) promptModeForAdmission(w http.ResponseWriter, r *http.Request, id string, intent *wire.SessionMode) (allowEmpty, ok bool) {
	authority, err := s.Store.ResolveEffectiveMode(r.Context(), id)
	if err != nil {
		if !requestscope.WriteSessionModeError(s.responses, w, err) {
			s.responses.InternalError(w, r, err)
		}
		return false, false
	}
	if intent != nil && authority.RootSessionID != id {
		requestscope.WriteSessionModeError(s.responses, w, store.ErrChildSessionMode)
		return false, false
	}
	mode, err := authority.WithModeIntent(intent)
	if requestscope.WriteSessionModeError(s.responses, w, err) {
		return false, false
	}
	// A first Ask prompt cannot answer the ambient workflow's intake.
	if mode == wire.SessionModeAsk && !authority.Started {
		return false, true
	}
	return s.Sessions.Runner.AcceptsEmpty(r.Context(), id), true
}
