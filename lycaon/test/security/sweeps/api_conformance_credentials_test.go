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
// unauthorized with nothing but the error envelope and change no host state,
// so a route registered without the bearer check fails here by name.
func (s *conformanceSweep) probeCredentials(ctx context.Context) {
	for _, op := range s.ops {
		if !op.Authenticated {
			continue
		}
		params, _ := s.values.pathParams(op)
		var reqs []conformanceRequest
		for _, refused := range refusedCredentials {
			req := conformanceRequest{op: op, probe: refused.probe, params: params, query: s.values.requiredQuery(op), credential: refused.credential}
			if op.JSONBody != nil {
				req.body, req.contentType = []byte("{}"), "application/json"
			}
			reqs = append(reqs, req)
		}
		changes := s.observed(ctx, op, "refused credentials", func() {
			for _, ex := range s.sendAll(ctx, reqs) {
				s.checkRefusedCredential(ex)
			}
		})
		if len(changes) > 0 {
			s.findings.add(ruleRequestsNeedTheBearer, op.ID, "refused credentials", "changed host state: %s", strings.Join(changes, ", "))
		}
	}
}

// observed runs send between a mark and a since and names what changed.
func (s *conformanceSweep) observed(ctx context.Context, op *conformanceOp, probe string, send func()) []string {
	if s.effects == nil {
		send()
		return nil
	}
	mark, err := s.effects.mark(ctx)
	if err != nil {
		s.findings.add(ruleRequestsNeedTheBearer, op.ID, probe, "could not observe host state: %v", err)
		send()
		return nil
	}
	send()
	changes, err := s.effects.since(ctx, mark)
	if err != nil {
		s.findings.add(ruleRequestsNeedTheBearer, op.ID, probe, "could not observe host state: %v", err)
		return nil
	}
	slices.Sort(changes)
	return slices.Compact(changes)
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
