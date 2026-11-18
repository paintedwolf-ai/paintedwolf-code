package specsync

import (
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// handlerErrorFixture is a module whose handlers use each pattern the scan
// resolves: a constant through a responder, a code-returning mapper and a
// code parameter, interface dispatch, a code carried in another package's
// error type (set directly or through a constructor's parameter), a table of
// codes, a closure, a handler registered through a wrapper, and details built
// by a wrapper and returned by a helper that adds a key conditionally.
var handlerErrorFixture = map[string]string{
	"lycaon/go.mod": "module github.com/lycaon/lycaon\n\ngo 1.26\n",
	"lycaon/pkg/api/api.go": `package api

type ApiErrorCode string

const (
	CodeNotFound ApiErrorCode = "widget_not_found"
	CodeConflict ApiErrorCode = "widget_conflict"
	CodeInvalid  ApiErrorCode = "invalid_request"
	CodeInternal ApiErrorCode = "internal_error"
	CodeLimited  ApiErrorCode = "rate_limited"
	CodeUnused   ApiErrorCode = "widget_gone"
	CodeMissing  ApiErrorCode = "widget_missing"
)

type ErrorResponse struct {
	Code    ApiErrorCode
	Message string
	Details map[string]any
}
`,
	"lycaon/internal/api/httpio/respond.go": `package httpio

import (
	"net/http"

	"github.com/lycaon/lycaon/pkg/api"
)

type Responder struct{}

func (Responder) body(code api.ApiErrorCode) api.ErrorResponse { return api.ErrorResponse{Code: code} }

func (s Responder) Fail(w http.ResponseWriter, code api.ApiErrorCode) { _ = s.body(code) }

func (s Responder) Internal(w http.ResponseWriter) { s.Fail(w, api.CodeInternal) }

func (s Responder) FailDetails(w http.ResponseWriter, code api.ApiErrorCode, details map[string]any) {
	body := s.body(code)
	body.Details = details
	_ = body
}

func (s Responder) FailReason(w http.ResponseWriter, code api.ApiErrorCode, reason string) {
	s.FailDetails(w, code, map[string]any{"reason": reason})
}
`,
	"lycaon/internal/domain/domain.go": `package domain

import "github.com/lycaon/lycaon/pkg/api"

// Error carries the code the HTTP layer answers.
type Error struct{ Code api.ApiErrorCode }

func (e *Error) Error() string { return string(e.Code) }

// Missing is built through a constructor that stores its parameter.
type Missing struct{ Code api.ApiErrorCode }

func (e *Missing) Error() string { return string(e.Code) }

func missing(code api.ApiErrorCode) error { return &Missing{Code: code} }

func Lookup(id string) error {
	switch id {
	case "":
		return &Error{Code: api.CodeConflict}
	case "gone":
		return missing(api.CodeMissing)
	}
	return nil
}

// Label names a code in a struct no error body reads.
type Label struct{ Code api.ApiErrorCode }

var Unused = Label{Code: api.CodeUnused}
`,
	"lycaon/internal/api/widgetadmin/handler.go": `package widgetadmin

import (
	"errors"
	"io"
	"net/http"

	"github.com/lycaon/lycaon/internal/domain"
	"github.com/lycaon/lycaon/pkg/api"
)

type failer interface {
	Fail(http.ResponseWriter, api.ApiErrorCode)
}

type Handler struct{ Responses failer }

var table = []struct {
	match error
	code  api.ApiErrorCode
}{{io.EOF, api.CodeInvalid}}

func (h *Handler) HandlePut(w http.ResponseWriter, r *http.Request) {
	err := domain.Lookup(r.URL.Path)
	var de *domain.Error
	if errors.As(err, &de) {
		h.Responses.Fail(w, de.Code)
		return
	}
	var dm *domain.Missing
	if errors.As(err, &dm) {
		h.Responses.Fail(w, dm.Code)
		return
	}
	for _, row := range table {
		if errors.Is(err, row.match) {
			h.Responses.Fail(w, row.code)
			return
		}
	}
	func() { h.Responses.Fail(w, api.CodeLimited) }()
}
`,
	"lycaon/internal/api/server.go": `package api

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/widgetadmin"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type operation struct{ ID string }

var (
	operationGetWidget   = operation{ID: "getWidget"}
	operationPutWidget   = operation{ID: "putWidget"}
	operationListWidgets = operation{ID: "listWidgets"}
)

type Server struct {
	responses httpio.Responder
	widgets   *widgetadmin.Handler
}

func (s *Server) registerV1Operation(_ *http.ServeMux, _ operation, _ http.HandlerFunc) {}

func (s *Server) routes(r *http.ServeMux) {
	s.registerV1Operation(r, operationGetWidget, s.handleGetWidget)
	s.registerV1Operation(r, operationPutWidget, s.widgets.HandlePut)
	s.registerV1Operation(r, operationListWidgets, s.recovered(s.handleListWidgets))
}

func (s *Server) handleGetWidget(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "":
		s.responses.Fail(w, wire.CodeNotFound)
		return
	case "reason":
		s.responses.FailReason(w, wire.CodeInvalid, "name is required")
		return
	case "details":
		s.responses.FailDetails(w, wire.CodeConflict, conflictDetails(r))
		return
	}
	s.responses.Internal(w)
}

func conflictDetails(r *http.Request) map[string]any {
	details := map[string]any{"field": "name"}
	if r.Method == http.MethodPut {
		details["hint"] = "rename"
	}
	return details
}

func (s *Server) recovered(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				s.responses.Internal(w)
			}
		}()
		h(w, r)
	}
}

func (s *Server) handleListWidgets(w http.ResponseWriter, r *http.Request) {
	s.fail(w, codeFor(r))
}

func codeFor(r *http.Request) wire.ApiErrorCode {
	if r.Method == http.MethodPost {
		return wire.CodeInvalid
	}
	return wire.CodeLimited
}

func (s *Server) fail(w http.ResponseWriter, code wire.ApiErrorCode) { s.responses.Fail(w, code) }
`,
}

// TestHandlerErrorScanResolvesEachPattern proves the scan's sensitivity on a
// fixture module and that the rule reports an emitted, undeclared status.
func TestHandlerErrorScanResolvesEachPattern(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for rel, body := range handlerErrorFixture {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		contractcheck.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(path), 0o755))
		contractcheck.FailErr(t, "write "+rel, os.WriteFile(path, []byte(body), 0o600))
	}
	tree := lycaonHandlers
	tree.register = map[string]bool{"(*" + lycaonModule + "/internal/api.Server).registerV1Operation": true}
	var prog *handlerErrorProgram
	var handlers []*handlerErrors
	res := invariants.Evaluate(invariants.LoadFixture(dir), invariants.Invariant{
		Name:    "scan",
		Anchors: []invariants.Anchor{invariants.Dir("lycaon/internal/api")},
		Check:   func(r *invariants.Run) { prog, handlers = handlerScan(r, tree) },
	})
	if res.Failed() {
		t.Fatalf("scan the fixture module:\n%s", res)
	}
	got := map[string][]string{}
	for _, h := range handlers {
		for code := range h.codes {
			got[h.operation] = append(got[h.operation], code)
		}
		if len(h.unresolved) > 0 {
			t.Errorf("%s: unresolved code sites %v", h.operation, h.unresolved)
		}
	}
	contractcheck.FailSetEqual(t, "getWidget codes", []string{"widget_not_found", "invalid_request", "widget_conflict", "internal_error"}, got["operationGetWidget"])
	contractcheck.FailSetEqual(t, "putWidget codes", []string{"widget_conflict", "widget_missing", "invalid_request", "rate_limited"}, got["operationPutWidget"])
	contractcheck.FailSetEqual(t, "listWidgets codes", []string{"invalid_request", "rate_limited", "internal_error"}, got["operationListWidgets"])

	statuses := map[string]int{"widget_not_found": 404, "widget_conflict": 409, "invalid_request": 400, "internal_error": 500, "rate_limited": 429}
	for _, h := range handlers {
		if h.operation != "operationListWidgets" {
			continue
		}
		violations := undeclaredErrorViolations("listWidgets", h, []string{"200", "400", "default"}, func(code string) int { return statuses[code] })
		contractcheck.FailSetEqual(t, "listWidgets violations",
			[]string{"listWidgets: " + h.handler + " emits rate_limited (429) but declares 200 400 default"}, violations)
	}

	details := prog.detailsOf(handlers)
	for code, keys := range map[string][]string{"invalid_request": {"reason"}, "widget_conflict": {"field", "hint"}} {
		var gotKeys []string
		for key := range details.keys[code] {
			gotKeys = append(gotKeys, key)
		}
		contractcheck.FailSetEqual(t, code+" details keys", keys, gotKeys)
	}
	notices := map[string]map[string]bool{"invalid_request": {"reason": true}, "widget_conflict": {"field": true}}
	contractcheck.FailSetEqual(t, "details violations",
		[]string{"widget_conflict: details.hint is not declared by its notice context_schema"},
		undeclaredDetailViolations(details, func(code string) (map[string]bool, bool) {
			keys, ok := notices[code]
			return keys, ok
		}))
}
