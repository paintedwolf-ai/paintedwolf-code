package sweeps

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
		prepared, err := s.successRequest(ctx, op)
		if err != nil {
			s.findings.add(rulePagination, op.ID, "page inputs", "could not prepare: %v", err)
			continue
		}
		params, base := prepared.params, prepared.query
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
// continuation, visit every row of each array limit bounds exactly once. A list
// holding fewer than two rows is seeded first.
func (s *conformanceSweep) walkPages(ctx context.Context) {
	for _, op := range s.ops {
		stream, pages := op.pageStream()
		limit := paramSchema(op.query("limit"))
		if !pages || limit == nil || limit.Max == nil {
			continue
		}
		base, err := s.successRequest(ctx, op)
		if err != nil {
			s.findings.add(rulePagination, op.ID, "walk", "could not prepare: %v", err)
			continue
		}
		page := func(probe string, n int64, cursor string) (conformanceExchange, map[string][]any) {
			query := url.Values{}
			for k, v := range base.query {
				query[k] = append([]string(nil), v...)
			}
			query.Set("limit", strconv.FormatInt(n, 10))
			if cursor != "" {
				query.Set(stream.cursor, cursor)
			}
			ex := s.send(ctx, conformanceRequest{op: op, probe: probe, params: base.params, query: query})
			return ex, stream.rows(op.Path, ex.body)
		}
		whole, members := page("limit at maximum", int64(*limit.Max), "")
		if whole.status/100 != 2 {
			s.findings.add(rulePagination, op.ID, "limit at maximum", "answered %s, want a page", whole.outcome())
			continue
		}
		if streamRows(members) < 2 {
			s.seedCollection(ctx, op)
			whole, members = page("limit at maximum", int64(*limit.Max), "")
		}
		if total := streamRows(members); total < 2 {
			s.findings.add(rulePagination, op.ID, "walk", "holds %d rows; paging needs at least 2", total)
			continue
		}
		s.outcomes.paged++
		seen := map[string]map[string]bool{}
		cursor := ""
		for step := 0; ; step++ {
			if step == pageWalkBound {
				s.findings.add(rulePagination, op.ID, "walk", "still continuing after %d pages", step)
				break
			}
			ex, rows := page("walk", 1, cursor)
			if ex.status/100 != 2 {
				s.findings.add(rulePagination, op.ID, "walk", "page %d answered %s", step, ex.outcome())
				break
			}
			for _, array := range slices.Sorted(maps.Keys(rows)) {
				if len(rows[array]) > 1 {
					s.findings.add(rulePagination, op.ID, "walk", "limit 1 answered %d rows%s", len(rows[array]), streamName(array))
				}
				if seen[array] == nil {
					seen[array] = map[string]bool{}
				}
				for _, row := range rows[array] {
					id := memberIdentity(row)
					if seen[array][id] {
						s.findings.add(rulePagination, op.ID, "walk", "row %s appeared twice%s", id, streamName(array))
					}
					seen[array][id] = true
				}
			}
			if cursor = stream.continuation(ex.body); cursor == "" {
				break
			}
		}
		if stream.continuation(whole.body) != "" {
			continue
		}
		for _, array := range slices.Sorted(maps.Keys(members)) {
			if len(seen[array]) != len(members[array]) {
				s.findings.add(rulePagination, op.ID, "walk", "visited %d rows%s; one page at the maximum holds %d", len(seen[array]), streamName(array), len(members[array]))
			}
		}
	}
}

// rows maps each array the stream pages to its rows in body.
func (st pageStream) rows(path string, body []byte) map[string][]any {
	if len(st.arrays) == 0 {
		return map[string][]any{"": listMembers(path, body)}
	}
	var envelope map[string]any
	_ = json.Unmarshal(body, &envelope)
	out := make(map[string][]any, len(st.arrays))
	for _, array := range st.arrays {
		out[array] = nestedRows(envelope, array)
	}
	return out
}

// nestedRows reads an array, or with "outer[].inner" the inner arrays of
// every outer member, end to end.
func nestedRows(envelope map[string]any, path string) []any {
	outer, inner, nested := strings.Cut(path, "[].")
	list, _ := envelope[outer].([]any)
	if !nested {
		return list
	}
	var rows []any
	for _, member := range list {
		if fields, ok := member.(map[string]any); ok {
			items, _ := fields[inner].([]any)
			rows = append(rows, items...)
		}
	}
	return rows
}

// continuation is the cursor body offers for the next page: a string, or a
// positive sequence number.
func (st pageStream) continuation(body []byte) string {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	switch next := envelope[st.next].(type) {
	case string:
		return next
	case float64:
		if next > 0 {
			return strconv.FormatInt(int64(next), 10)
		}
	}
	return ""
}

func streamRows(rows map[string][]any) int {
	total := 0
	for _, list := range rows {
		total += len(list)
	}
	return total
}

func streamName(array string) string {
	if array == "" {
		return ""
	}
	return " in " + array
}

// seedCollection runs op's seed recipe, or repeats the create that adds
// members to op's collection.
func (s *conformanceSweep) seedCollection(ctx context.Context, op *conformanceOp) {
	if seed := successRecipes[op.ID].seed; seed != nil {
		if err := seed(ctx, s); err != nil {
			s.findings.add(rulePagination, op.ID, "seed", "%v", err)
		}
		return
	}
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
	// Otherwise the only non-empty array, so an always-empty side list
	// cannot stand in for the rows.
	var rows []any
	for _, key := range slices.Sorted(maps.Keys(envelope)) {
		if list, ok := envelope[key].([]any); ok && len(list) > 0 {
			if rows != nil {
				return nil
			}
			rows = list
		}
	}
	return rows
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
