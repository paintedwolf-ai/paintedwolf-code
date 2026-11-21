package sweeps

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// checkResponse: every response has a status its operation declares and a body
// that validates against that declaration; an error body is the envelope whose
// code's vocabulary status is the status answered. A probe never earns a 500.
// It reports whether the answer matched its declaration.
func (s *conformanceSweep) checkResponse(ctx context.Context, ex conformanceExchange) bool {
	op := ex.req.op
	valid := false
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
		valid = err == nil
	}
	if ex.status < 400 {
		return valid
	}
	if ex.apiErr == nil {
		s.findings.add(ruleResponsesConform, op.ID, ex.req.probe, "%d body is not the error envelope", ex.status)
		return false
	}
	if !slices.Contains(wire.AllApiErrorCodeValues(), ex.apiErr.Code) {
		s.findings.add(ruleResponsesConform, op.ID, ex.req.probe, "code %q is not in the ApiErrorCode vocabulary", ex.apiErr.Code)
		return false
	}
	if want := ex.apiErr.Code.HTTPStatus(); want != ex.status {
		s.findings.add(ruleResponsesConform, op.ID, ex.req.probe, "code %s declares status %d but answered %d", ex.apiErr.Code, want, ex.status)
		return false
	}
	return valid
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

// requestCredential is the Authorization a sweep request carries.
type requestCredential int

const (
	credentialValid requestCredential = iota
	credentialNone
	credentialWrong
)

var refusedCredentials = []struct {
	probe      string
	credential requestCredential
}{
	{"no credential", credentialNone},
	{"wrong credential", credentialWrong},
}

// probeCredentials sends every operation that requires a credential with none
// and with a wrong one, addressed at real fixtures. Each must answer 401
// unauthorized with nothing but the error envelope, and the whole batch must
// change no host state, so a route registered without the bearer check fails
// here by name.
func (s *conformanceSweep) probeCredentials(ctx context.Context) {
	var reqs []conformanceRequest
	for _, op := range s.ops {
		if !op.Authenticated {
			continue
		}
		params, _ := s.values.pathParams(op)
		for _, refused := range refusedCredentials {
			req := conformanceRequest{op: op, probe: refused.probe, params: params, query: s.values.requiredQuery(op), credential: refused.credential}
			if op.JSONBody != nil {
				req.body, req.contentType = []byte("{}"), "application/json"
			}
			reqs = append(reqs, req)
		}
	}
	const probe = "unauthenticated requests"
	if s.effects == nil {
		s.checkRefusedCredentials(ctx, reqs)
		return
	}
	mark, err := s.effects.mark(ctx)
	if err != nil {
		s.findings.add(ruleRequestsNeedTheBearer, "sweep", probe, "could not observe host state: %v", err)
		return
	}
	s.checkRefusedCredentials(ctx, reqs)
	changes, err := s.effects.since(ctx, mark)
	switch {
	case err != nil:
		s.findings.add(ruleRequestsNeedTheBearer, "sweep", probe, "could not observe host state: %v", err)
	case len(changes) > 0:
		slices.Sort(changes)
		s.findings.add(ruleRequestsNeedTheBearer, "sweep", probe, "changed host state: %s", strings.Join(slices.Compact(changes), ", "))
	}
}

func (s *conformanceSweep) checkRefusedCredentials(ctx context.Context, reqs []conformanceRequest) {
	for _, ex := range s.sendAll(ctx, reqs) {
		s.checkRefusedCredential(ex)
	}
}

func (s *conformanceSweep) checkRefusedCredential(ex conformanceExchange) {
	op := ex.req.op
	if ex.status != http.StatusUnauthorized || ex.code() != string(wire.ApiErrorCodeUnauthorized) {
		s.findings.add(ruleRequestsNeedTheBearer, op.ID, ex.req.probe, "answered %s, want 401 unauthorized", ex.outcome())
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(ex.body, &body) != nil {
		return
	}
	var extra []string
	for key := range body {
		if !errorEnvelopeFields[key] {
			extra = append(extra, key)
		}
	}
	if ex.apiErr != nil && len(ex.apiErr.Details) > 0 {
		extra = append(extra, "details")
	}
	if len(extra) > 0 {
		slices.Sort(extra)
		s.findings.add(ruleRequestsNeedTheBearer, op.ID, ex.req.probe, "carries fields beyond the error envelope: %s", strings.Join(extra, ", "))
	}
}

// errorEnvelopeFields are the JSON names of the error envelope.
var errorEnvelopeFields = func() map[string]bool {
	out := map[string]bool{}
	envelope := reflect.TypeFor[wire.ErrorResponse]()
	for i := range envelope.NumField() {
		name, _, _ := strings.Cut(envelope.Field(i).Tag.Get("json"), ",")
		out[name] = true
	}
	return out
}()
