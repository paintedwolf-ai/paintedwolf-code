package security

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// pageProbe is one invalid page input and the parameter it must be named by.
type pageProbe struct {
	param string
	code  string
}

// probePagination checks cursor errors and declared limit bounds on JSON lists.
func (s *conformanceSweep) probePagination(ctx context.Context) {
	var reqs []conformanceRequest
	var probes []pageProbe
	for _, op := range s.ops {
		pages := op.pageParams()
		if len(pages) == 0 {
			continue
		}
		params, _ := s.values.pathParams(op)
		base := s.values.requiredQuery(op)
		add := func(probe, param, value, code string) {
			query := url.Values{}
			for k, v := range base {
				query[k] = append([]string(nil), v...)
			}
			query.Set(param, value)
			reqs = append(reqs, conformanceRequest{op: op, probe: probe, params: params, query: query})
			probes = append(probes, pageProbe{param: param, code: code})
		}
		for _, name := range pages {
			add("foreign "+name, name, "conformance-not-a-cursor", "invalid_page_cursor")
		}
		limit := paramSchema(op.query("limit"))
		switch {
		case limit == nil:
			s.findings.add(rulePagination, op.ID, "limit", "pages with %s but declares no limit", strings.Join(pages, "/"))
		case limit.Min == nil || limit.Max == nil:
			s.findings.add(rulePagination, op.ID, "limit", "limit declares no minimum or maximum")
		default:
			add("limit above maximum", "limit", strconv.FormatInt(int64(*limit.Max)+1, 10), "invalid_query")
			add("limit below minimum", "limit", strconv.FormatInt(int64(*limit.Min)-1, 10), "invalid_query")
		}
	}
	for n, ex := range s.sendAll(ctx, reqs) {
		s.checkPageAnswer(ex, probes[n])
	}
}

func (s *conformanceSweep) checkPageAnswer(ex conformanceExchange, want pageProbe) {
	param, _ := detailString(ex, "param")
	if ex.status == http.StatusBadRequest && ex.code() == want.code && param == want.param {
		return
	}
	_, known := s.values.pathParams(ex.req.op)
	for _, k := range known {
		if !k && ex.status == http.StatusNotFound && strings.HasSuffix(ex.code(), "_not_found") {
			return
		}
	}
	s.findings.add(rulePagination, ex.req.op.ID, ex.req.probe, "answered %s (details.param %q), want 400 %s naming %q",
		ex.outcome(), param, want.code, want.param)
}

func detailString(ex conformanceExchange, key string) (string, bool) {
	if ex.apiErr == nil {
		return "", false
	}
	value, ok := ex.apiErr.Details[key].(string)
	return value, ok
}
