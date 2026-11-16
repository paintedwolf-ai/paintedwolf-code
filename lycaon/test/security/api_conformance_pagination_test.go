package security

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
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

// pageWalkBound stops a walk whose cursor never ends.
const pageWalkBound = 256

// continuationField names the response field that carries the cursor a page
// parameter takes.
func continuationField(param string) string {
	if param == "cursor" {
		return "next_cursor"
	}
	return param + "_cursor"
}

// walkPages checks every paged list against its own rows: the declared
// maximum is accepted, and pages of one row each, followed by their
// continuation, visit every row exactly once. A list holding fewer than two
// rows is seeded by its collection's create first.
func (s *conformanceSweep) walkPages(ctx context.Context) {
	for _, op := range s.ops {
		pages := op.pageParams()
		limit := paramSchema(op.query("limit"))
		if len(pages) == 0 || limit == nil || limit.Max == nil {
			continue
		}
		params, _ := s.values.pathParams(op)
		page := func(probe string, n int64, param, cursor string) (conformanceExchange, []any) {
			query := s.values.requiredQuery(op)
			query.Set("limit", strconv.FormatInt(n, 10))
			if cursor != "" {
				query.Set(param, cursor)
			}
			ex := s.send(ctx, conformanceRequest{op: op, probe: probe, params: params, query: query})
			return ex, listMembers(op.Path, ex.body)
		}
		whole, members := page("limit at maximum", int64(*limit.Max), "", "")
		if whole.status/100 != 2 {
			s.findings.add(rulePagination, op.ID, "limit at maximum", "answered %s, want a page", whole.outcome())
			continue
		}
		if len(members) < 2 {
			s.seedCollection(ctx, op)
			whole, members = page("limit at maximum", int64(*limit.Max), "", "")
		}
		if len(members) < 2 {
			s.findings.add(rulePagination, op.ID, "walk", "holds %d rows; paging needs at least 2", len(members))
			continue
		}
		s.paged++
		param := pages[0]
		seen := map[string]bool{}
		cursor := ""
		for step := 0; ; step++ {
			if step == pageWalkBound {
				s.findings.add(rulePagination, op.ID, "walk", "still continuing after %d pages", step)
				break
			}
			ex, rows := page("walk", 1, param, cursor)
			if ex.status/100 != 2 {
				s.findings.add(rulePagination, op.ID, "walk", "page %d answered %s", step, ex.outcome())
				break
			}
			if len(rows) > 1 {
				s.findings.add(rulePagination, op.ID, "walk", "limit 1 answered %d rows", len(rows))
			}
			for _, row := range rows {
				id := memberIdentity(row)
				if seen[id] {
					s.findings.add(rulePagination, op.ID, "walk", "row %s appeared twice", id)
				}
				seen[id] = true
			}
			cursor = continuation(ex.body, param)
			if cursor == "" {
				break
			}
		}
		if hasMore := continuation(whole.body, param) != ""; !hasMore && len(seen) != len(members) {
			s.findings.add(rulePagination, op.ID, "walk", "visited %d rows; one page at the maximum holds %d", len(seen), len(members))
		}
	}
}

// seedCollection repeats the create that adds members to op's collection.
func (s *conformanceSweep) seedCollection(ctx context.Context, op *conformanceOp) {
	for _, create := range s.ops {
		if create.Method != http.MethodPost || create.Path != op.Path {
			continue
		}
		for range 2 {
			req, err := s.successRequest(ctx, create)
			if err != nil {
				return
			}
			req.probe = "seed " + op.ID
			if ex := s.send(ctx, req); ex.status/100 == 2 {
				s.bindCreated(create, ex)
			}
		}
	}
}

// listMembers is the list envelope's array, preferring the one named for the
// collection's last segment.
func listMembers(path string, body []byte) []any {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	segments := strings.Split(path, "/")
	if list, ok := envelope[strings.ReplaceAll(segments[len(segments)-1], "-", "_")].([]any); ok {
		return list
	}
	for _, key := range slices.Sorted(maps.Keys(envelope)) {
		if list, ok := envelope[key].([]any); ok {
			return list
		}
	}
	return nil
}

// memberIdentity names a row by its id, or by its whole value.
func memberIdentity(row any) string {
	if fields, ok := row.(map[string]any); ok {
		if id, ok := fields["id"].(string); ok && id != "" {
			return id
		}
	}
	raw, _ := json.Marshal(row)
	return string(raw)
}

func continuation(body []byte, param string) string {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	cursor, _ := envelope[continuationField(param)].(string)
	return cursor
}
