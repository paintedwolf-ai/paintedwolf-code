package security

import (
	"context"
	"net/http"
	"slices"
	"strings"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// checkResponse: every response has a status its operation declares and a body
// that validates against that declaration; an error body is the envelope whose
// code's vocabulary status is the status answered. A probe never earns a 500.
func (s *conformanceSweep) checkResponse(ctx context.Context, ex conformanceExchange) {
	op := ex.req.op
	switch {
	case ex.status == http.StatusInternalServerError:
		s.findings.add(ruleResponsesConform, op.ID, ex.req.probe, "answered %s", ex.outcome())
	case !op.declares(ex.status):
		s.findings.add(ruleResponsesConform, op.ID, ex.req.probe, "answered undeclared status %s", ex.outcome())
	default:
		err := s.validator.ValidateResponse(ctx, op.Method, op.Path, ex.req.params, ex.status, ex.header, ex.body)
		if err != nil {
			s.findings.add(ruleResponsesConform, op.ID, ex.req.probe, "%s does not match its declaration: %s", ex.outcome(), firstLine(err.Error()))
		}
	}
	if ex.status < 400 {
		return
	}
	if ex.apiErr == nil {
		s.findings.add(ruleResponsesConform, op.ID, ex.req.probe, "%d body is not the error envelope", ex.status)
		return
	}
	if !slices.Contains(wire.AllApiErrorCodeValues(), ex.apiErr.Code) {
		s.findings.add(ruleResponsesConform, op.ID, ex.req.probe, "code %q is not in the ApiErrorCode vocabulary", ex.apiErr.Code)
		return
	}
	if want := ex.apiErr.Code.HTTPStatus(); want != ex.status {
		s.findings.add(ruleResponsesConform, op.ID, ex.req.probe, "code %s declares status %d but answered %d", ex.apiErr.Code, want, ex.status)
	}
}

// checkErrorCopy compares error copy and detail keys with the notice catalog.
func (s *conformanceSweep) checkErrorCopy(ex conformanceExchange) {
	if ex.apiErr == nil || s.notices == nil {
		return
	}
	op, apiErr := ex.req.op, ex.apiErr
	want := s.notices.RenderWire(string(apiErr.Code), apiErr.Details)
	for _, field := range []struct{ name, got, want string }{
		{"message", apiErr.Message, want.Message},
		{"title", apiErr.Title, want.Title},
		{"suggested_action", apiErr.SuggestedAction, want.SuggestedAction},
	} {
		if field.got != field.want {
			s.findings.add(ruleMessageIsNoticeCopy, op.ID, ex.req.probe, "%s %s is %q, not the notice copy %q", apiErr.Code, field.name, field.got, field.want)
		}
	}
	declared := s.noticeContext(string(apiErr.Code))
	for key := range apiErr.Details {
		if !declared[key] {
			s.findings.add(ruleDetailsAreDeclared, op.ID, ex.req.probe, "%s details.%s is not declared by its notice context_schema", apiErr.Code, key)
		}
	}
}

// noticeContext is the set of render variables code's notice declares.
func (s *conformanceSweep) noticeContext(code string) map[string]bool {
	out := map[string]bool{}
	cfg := s.notices.Config()
	if cfg == nil {
		return out
	}
	schema := cfg.UserNotices[code].ContextSchema
	for _, group := range []string{"required", "optional"} {
		list, _ := schema[group].([]any)
		for _, item := range list {
			if name, ok := item.(string); ok {
				out[strings.TrimSpace(name)] = true
			}
		}
	}
	return out
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 240 {
		line = line[:240] + "…"
	}
	return line
}
