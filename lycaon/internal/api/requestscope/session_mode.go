package requestscope

import (
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func WriteSessionModeError(responses *httpio.Responder, w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrChildSessionMode):
		responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "Worker chats inherit the root chat’s mode.")
	case errors.Is(err, store.ErrInvalidSessionMode):
		responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "Choose Agent or Ask mode.")
	case errors.Is(err, store.ErrSessionModeLocked):
		responses.Fail(w, wire.ApiErrorCodeSessionModeLocked, "This chat has started in Agent mode. Open a new chat to use Ask.")
	case errors.Is(err, store.ErrSessionModeUnavailable):
		responses.Fail(w, wire.ApiErrorCodeSessionModeUnavailable, "Chat mode is unavailable. Reload the chat or start a new one.")
	default:
		return false
	}
	return true
}
