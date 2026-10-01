package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/people/personactions"
)

// recordPersonAction records each completed call of an operation the catalog
// declares with x-person-action: record. The record follows the handler, so a
// failed write is logged rather than undoing the operation.
func (s *Server) recordPersonAction(operation generatedOperation, next http.HandlerFunc) http.HandlerFunc {
	if !operation.RecordsPersonAction {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, subject := personactions.WithSubject(r.Context())
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next(ww, r.WithContext(ctx))
		s.writePersonAction(r, operation, subject, ww.Status())
	}
}

func (s *Server) writePersonAction(r *http.Request, operation generatedOperation, subject *personactions.Subject, status int) {
	if s.personActions == nil {
		return
	}
	caller, ok := people.Caller(r.Context())
	if !ok {
		return
	}
	if status == 0 {
		status = http.StatusOK
	}
	params := make(map[string]string, len(operation.Params))
	for _, name := range operation.Params {
		params[name] = chi.URLParam(r, name)
	}
	ctx := context.WithoutCancel(r.Context())
	if err := s.personActions.Record(ctx, personactions.Action{
		PersonID: caller.ID, OperationID: operation.ID, PathParams: params,
		Subject: subject.Values(), Status: status,
	}); err != nil {
		slog.ErrorContext(ctx, "record person action", "operation_id", operation.ID, "error", err)
	}
}
