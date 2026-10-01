package security

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// conformanceOversizeBytes exceeds every body bound the host enforces below
// it (the largest, prompt attachment uploads, is 128 MiB); the stream is
// generated as it is read, so a bounded handler stops it at its bound. Larger
// bounds are declared as the body schema's maxLength.
const conformanceOversizeBytes = 512 << 20

// bodyExpectation is the answer a request-body probe requires.
type bodyExpectation struct {
	statuses []int
	codes    []string
}

func (want bodyExpectation) accepts(ex conformanceExchange) bool {
	statusOK, codeOK := false, len(want.codes) == 0
	for _, status := range want.statuses {
		statusOK = statusOK || ex.status == status
	}
	for _, code := range want.codes {
		codeOK = codeOK || ex.code() == code
	}
	return statusOK && codeOK
}

func (want bodyExpectation) String() string {
	var parts []string
	for _, status := range want.statuses {
		parts = append(parts, strconv.Itoa(status))
	}
	out := strings.Join(parts, " or ")
	if len(want.codes) > 0 {
		out += " " + strings.Join(want.codes, "|")
	}
	return out
}

// probeRequestBodies checks media types, size limits, and JSON shape.
func (s *conformanceSweep) probeRequestBodies(ctx context.Context) {
	var reqs []conformanceRequest
	var wants []bodyExpectation
	var oversized []*conformanceOp
	for _, op := range s.ops {
		if len(op.BodyMedia) == 0 {
			continue
		}
		params, _ := s.values.pathParams(op)
		query := s.values.requiredQuery(op)
		add := func(probe, body, contentType string, want bodyExpectation) {
			reqs = append(reqs, conformanceRequest{op: op, probe: probe, params: params, query: query, body: []byte(body), contentType: contentType})
			wants = append(wants, want)
		}
		add("unsupported media type", "{}", "application/x-conformance",
			bodyExpectation{[]int{http.StatusUnsupportedMediaType}, []string{"unsupported_media_type"}})
		if op.Declared[http.StatusRequestEntityTooLarge] {
			oversized = append(oversized, op)
		}
		if op.JSONBody == nil {
			continue
		}
		invalidJSON := bodyExpectation{[]int{http.StatusBadRequest}, []string{"invalid_json"}}
		add("malformed JSON", "{", "application/json", invalidJSON)
		if !opensAdditionalProperties(op) {
			add("unknown field", `{"conformance_unknown_field":true}`, "application/json", invalidJSON)
		}
		if len(op.JSONBody.Required) > 0 {
			add("empty object", "{}", "application/json",
				bodyExpectation{[]int{http.StatusBadRequest, http.StatusUnprocessableEntity}, nil})
		}
	}
	for n, ex := range s.sendAll(ctx, reqs) {
		s.checkBodyAnswer(ex, wants[n])
	}
	// Any 413 code will do: an upload may name its own bound.
	tooLarge := bodyExpectation{[]int{http.StatusRequestEntityTooLarge}, nil}
	for _, op := range oversized {
		params, _ := s.values.pathParams(op)
		ex := s.send(ctx, s.oversizedRequest(op, params))
		s.checkBodyAnswer(ex, tooLarge)
	}
}

func (s *conformanceSweep) checkBodyAnswer(ex conformanceExchange, want bodyExpectation) {
	if want.accepts(ex) {
		return
	}
	_, known := s.values.pathParams(ex.req.op)
	for _, k := range known {
		if !k && ex.status == http.StatusNotFound && strings.HasSuffix(ex.code(), "_not_found") {
			return
		}
	}
	s.findings.add(ruleRequestBodies, ex.req.op.ID, ex.req.probe, "answered %s, want %s", ex.outcome(), want)
}

// opensAdditionalProperties reports a map-typed body, whose top-level keys are
// arbitrary by declaration.
func opensAdditionalProperties(op *conformanceOp) bool {
	extra := op.JSONBody.AdditionalProperties
	return extra.Schema != nil || (extra.Has != nil && *extra.Has)
}

func (s *conformanceSweep) oversizedRequest(op *conformanceOp, params map[string]string) conformanceRequest {
	contentType, prefix := op.BodyMedia[0], ""
	if op.JSONBody != nil {
		contentType, prefix = "application/json", `{"conformance_oversized":"`
	}
	stream := io.MultiReader(strings.NewReader(prefix), io.LimitReader(repeatingReader('a'), conformanceOversizeBytes))
	req := conformanceRequest{op: op, probe: "oversized body", params: params, query: s.values.requiredQuery(op), stream: stream, contentType: contentType}
	// A declared bound beyond the streamed probe is exceeded by announcement:
	// the host refuses the declared length before reading.
	if op.BodyMaxBytes >= conformanceOversizeBytes {
		req.announcedLength = int64(op.BodyMaxBytes) + 1
	}
	return req
}

// repeatingReader yields one byte forever.
type repeatingReader byte

func (r repeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}
