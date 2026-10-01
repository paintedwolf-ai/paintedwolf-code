package security

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

// notFoundAnswer is the code one probe answered for an unknown resource
// address.
type notFoundAnswer struct {
	op     *conformanceOp
	param  string
	prefix string
	code   string
}

// probeUnknownResources checks each resource path's declared not-found code.
func (s *conformanceSweep) probeUnknownResources(ctx context.Context) {
	var reqs []conformanceRequest
	var targets []int
	for _, op := range s.ops {
		for i, p := range op.PathParams {
			if !op.namesResource(i) {
				continue
			}
			params, _ := s.values.pathParams(op)
			params[p.Name] = freshValue(paramSchema(p))
			reqs = append(reqs, s.validBodyRequest(op, "unknown {"+p.Name+"}", params))
			targets = append(targets, i)
		}
	}
	var answers []notFoundAnswer
	for n, ex := range s.sendAll(ctx, reqs) {
		if answer, ok := s.checkNotFound(ex, targets[n]); ok {
			answers = append(answers, answer)
		}
	}
	s.checkNotFoundAgreement(answers)
}

// validBodyRequest attaches a spec-valid body when the operation requires one,
// so the unknown address is the only thing wrong with the request.
func (s *conformanceSweep) validBodyRequest(op *conformanceOp, probe string, params map[string]string) conformanceRequest {
	query := s.values.requiredQuery(op)
	switch {
	case !op.BodyRequired:
		return conformanceRequest{op: op, probe: probe, params: params, query: query}
	case op.JSONBody != nil:
		return jsonRequest(op, probe, params, query, s.values.body(op.JSONBody, nil))
	default:
		return conformanceRequest{op: op, probe: probe, params: params, query: query, body: []byte("conformance"), contentType: op.BodyMedia[0]}
	}
}

func (s *conformanceSweep) checkNotFound(ex conformanceExchange, target int) (notFoundAnswer, bool) {
	op := ex.req.op
	_, known := s.values.pathParams(op)
	// The first address the request does not know answers.
	answering := target
	for j := range target {
		if !known[j] {
			answering = j
			break
		}
	}
	if op.upsertTarget(target) && answering == target && ex.status/100 == 2 {
		return notFoundAnswer{}, false
	}
	code := ex.code()
	switch {
	case ex.status != http.StatusNotFound:
		s.findings.add(ruleUnknownResources, op.ID, ex.req.probe, "answered %s, want 404 <resource>_not_found", ex.outcome())
		return notFoundAnswer{}, false
	case !op.Declared[http.StatusNotFound]:
		s.findings.add(ruleUnknownResources, op.ID, ex.req.probe, "answered 404 %s but the operation does not declare 404", code)
	}
	if code == "not_found" || !strings.HasSuffix(code, "_not_found") {
		s.findings.add(ruleUnknownResources, op.ID, ex.req.probe, "answered 404 %q, which does not name the resource", code)
		return notFoundAnswer{}, false
	}
	return notFoundAnswer{op: op, param: op.PathParams[answering].Name, prefix: op.prefix(answering), code: code}, true
}

// checkNotFoundAgreement requires one not-found code per resource address. The
// address's own GET is authoritative; otherwise the most common code is.
func (s *conformanceSweep) checkNotFoundAgreement(answers []notFoundAnswer) {
	byPrefix := map[string][]notFoundAnswer{}
	for _, a := range answers {
		byPrefix[a.prefix] = append(byPrefix[a.prefix], a)
	}
	for prefix, group := range byPrefix {
		canonical, source := canonicalNotFound(prefix, group)
		for _, a := range group {
			if a.code != canonical {
				s.findings.add(ruleUnknownResources, a.op.ID, "unknown {"+a.param+"}",
					"answered %s, but an unknown %s answers %s (%s)", a.code, prefix, canonical, source)
			}
		}
	}
}

func canonicalNotFound(prefix string, group []notFoundAnswer) (code, source string) {
	counts := map[string]int{}
	for _, a := range group {
		if a.op.Method == http.MethodGet && a.op.Path == prefix {
			return a.code, a.op.ID
		}
		counts[a.code]++
	}
	codes := make([]string, 0, len(counts))
	for c := range counts {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool {
		if counts[codes[i]] != counts[codes[j]] {
			return counts[codes[i]] > counts[codes[j]]
		}
		return codes[i] < codes[j]
	})
	return codes[0], "most operations under it"
}
