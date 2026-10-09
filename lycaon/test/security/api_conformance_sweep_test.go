package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/test/wiring"
	openapi "github.com/lycaon/lycaon/test/openapi"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// conformanceRule names one runtime invariant of the sweep.
type conformanceRule string

const (
	ruleResponsesConform      conformanceRule = "responses_conform"
	ruleUnknownResources      conformanceRule = "unknown_resources_answer_not_found"
	ruleRequestBodies         conformanceRule = "request_bodies_are_validated"
	rulePagination            conformanceRule = "page_inputs_are_validated"
	ruleReadsHaveNoEffects    conformanceRule = "reads_have_no_side_effects"
	ruleNoSecretMaterial      conformanceRule = "no_secret_material_escapes"
	ruleMessageIsNoticeCopy   conformanceRule = "error_message_is_notice_copy"
	ruleDetailsAreDeclared    conformanceRule = "error_details_are_declared"
	ruleEventScopesAreHostIDs conformanceRule = "event_scopes_name_host_resources"
)

var conformanceRules = []conformanceRule{
	ruleResponsesConform, ruleUnknownResources, ruleRequestBodies, rulePagination,
	ruleReadsHaveNoEffects, ruleNoSecretMaterial, ruleMessageIsNoticeCopy,
	ruleDetailsAreDeclared, ruleEventScopesAreHostIDs,
}

// conformanceRequestBudget bounds one request so a handler that never returns
// fails the sweep instead of hanging it.
const conformanceRequestBudget = time.Minute

// TestAPIConformance drives every operation in docs/openapi.yaml against one
// production-wired server and reports each runtime invariant as a subtest.
// Every violation is `<operationId|topic>: <probe>: <detail>`.
func TestAPIConformance(t *testing.T) {
	for _, key := range []string{
		"LYCAON_RATE_SESSIONS_PER_MIN", "LYCAON_RATE_PROMPTS_PER_MIN", "LYCAON_RATE_WRITES_PER_MIN",
		"LYCAON_RATE_SOURCE_PER_MIN", "LYCAON_RATE_EDITOR_PER_MIN", "LYCAON_RATE_PRESENCE_PER_MIN",
	} {
		// Admission limits are their own contract; the sweep sends many writes.
		t.Setenv(key, "100000000")
	}
	h := wiring.BuildForTest(t)
	hub := h.MemoryHub()
	if hub == nil {
		t.Fatal("conformance sweep needs the in-memory event hub")
	}
	capture := startConformanceEventCapture(t, hub)

	projectDir := h.ProjectDir(t, "conformance")
	testutil.FailErr(t, "write fixture file", os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("conformance\n"), 0o644))
	project := createTestProjectHTTP(t, h.Server, projectDir)
	sess := createSessionForProjectHTTP(t, h.Server, project.ID, wire.SessionPostureBuild)
	provider := createConformanceProvider(t, h.Server)

	validator, err := openapi.Bundled()
	testutil.FailErr(t, "load the bundled OpenAPI document", err)
	doc, err := openapi.Document()
	testutil.FailErr(t, "load the bundled OpenAPI document", err)
	noticeCfg, err := usernotice.LoadEffectiveUserNotices(extpacks.Active())
	testutil.FailErr(t, "load the server's notice catalog", err)

	sweep := newConformanceSweep(doc, validator, h.Server, usernotice.NewCatalog(noticeCfg))
	settleSourceWatcher(t, hub, projectDir)
	sweep.Effects = openConformanceEffects(t, h, hub)
	bindConformanceFixtures(sweep.values, project, sess, "README.md")
	sweep.values.bind("/v1/providers/{provider_id}", "", provider)
	// A keyed web research provider.
	sweep.values.bind("/v1/web-research/providers/{provider_id}", "", string(wire.WebSearchProviderTavily))
	sweep.runAll(t.Context())

	hub.FlushDebounced()
	events := capture.stop(t)
	sweep.checkEventScopes(events, conformanceHostLookup(t, h))
	sweep.secrets.scanEvents(sweep.findings, events)
	sweep.report(t)
}

// bindConformanceFixtures binds the fixture project and chat by their
// addresses (host contract §11: /v1/projects/{id}, /v1/sessions/{id}) and the
// semantic query and property values that name them.
func bindConformanceFixtures(values *conformanceValues, project wire.Project, sess wire.Session, file string) {
	values.bind("/v1/projects/{id}", "project_id", project.ID)
	values.bind("/v1/sessions/{id}", "session_id", sess.ID)
	if len(project.Roots) > 0 {
		values.bind("", "root_id", project.Roots[0].ID)
	}
	values.bind("", "path", file)
}

// createConformanceProvider registers a model provider that takes an API key,
// so the credential route has a real provider to hold a secret for.
func createConformanceProvider(t *testing.T, srv http.Handler) string {
	t.Helper()
	const id = "conformance-provider"
	req := authedRequest(t, http.MethodPost, "/v1/providers",
		strings.NewReader(`{"id":"`+id+`","base_url":"https://example.invalid/v1","requires_api_key":true}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create provider fixture status = %d body = %s", w.Code, w.Body.String())
	}
	return id
}

// conformanceSweep holds one spec, one server, and the findings of every rule.
type conformanceSweep struct {
	ops       []*conformanceOp
	validator *openapi.Validator
	handler   http.Handler
	notices   *usernotice.Catalog
	values    *conformanceValues
	effects   conformanceEffects
	secrets   *conformanceSecrets
	findings  *conformanceFindings
	// children maps a collection path to the path parameters addressed below it.
	children map[string][]string
}

func newConformanceSweep(doc *openapi3.T, validator *openapi.Validator, handler http.Handler, notices *usernotice.Catalog) *conformanceSweep {
	s := &conformanceSweep{
		ops: conformanceOperations(doc), validator: validator, handler: handler, notices: notices,
		values: newConformanceValues(), secrets: &conformanceSecrets{}, findings: newConformanceFindings(),
		children: map[string][]string{},
	}
	seen := map[string]bool{}
	for _, op := range s.ops {
		for i, p := range op.PathParams {
			collection := strings.TrimSuffix(op.prefix(i), "/{"+p.Name+"}")
			if key := collection + "\x00" + p.Name; !seen[key] {
				seen[key] = true
				s.children[collection] = append(s.children[collection], p.Name)
			}
		}
	}
	return s
}

// runAll runs every phase in dependency order: reads discover fixtures and
// prove read purity; secrets are seeded and reads repeated so every later
// response is scanned for them; the probes then run against real fixtures.
func (s *conformanceSweep) runAll(ctx context.Context) {
	s.sweepReads(ctx)
	s.seedSecrets(ctx)
	s.sweepReads(ctx)
	s.probePagination(ctx)
	s.probeUnknownResources(ctx)
	s.probeRequestBodies(ctx)
}

// report prints each rule's violations, sorted, as its own subtest.
func (s *conformanceSweep) report(t *testing.T) {
	t.Helper()
	for _, note := range s.findings.notes() {
		t.Log(note)
	}
	for _, rule := range conformanceRules {
		t.Run(string(rule), func(t *testing.T) {
			violations := s.findings.list(rule)
			for _, v := range violations {
				t.Errorf("%s", v)
			}
			if len(violations) > 0 {
				t.Logf("%s: %d violations", rule, len(violations))
			}
		})
	}
}

// conformanceRequest is one request the sweep sends.
type conformanceRequest struct {
	op     *conformanceOp
	probe  string
	params map[string]string
	query  url.Values
	// body is sent when non-nil; stream replaces it for oversized bodies.
	body   []byte
	stream io.Reader
	// announcedLength, when positive, is the Content-Length the request declares.
	announcedLength int64
	contentType     string
}

// conformanceExchange is one observed response.
type conformanceExchange struct {
	req    conformanceRequest
	status int
	header http.Header
	body   []byte
	// apiErr is the decoded error envelope of a 4xx/5xx JSON body.
	apiErr *conformanceAPIError
}

type conformanceAPIError struct {
	Code            wire.ApiErrorCode `json:"code"`
	Message         string            `json:"message"`
	Title           string            `json:"title"`
	SuggestedAction string            `json:"suggested_action"`
	Details         map[string]any    `json:"details"`
}

func (ex conformanceExchange) code() string {
	if ex.apiErr == nil {
		return ""
	}
	return string(ex.apiErr.Code)
}

// outcome is how a violation names the response.
func (ex conformanceExchange) outcome() string {
	if code := ex.code(); code != "" {
		return fmt.Sprintf("%d %s", ex.status, code)
	}
	return fmt.Sprintf("%d", ex.status)
}

func jsonRequest(op *conformanceOp, probe string, params map[string]string, query url.Values, body any) conformanceRequest {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(fmt.Sprintf("encode synthesized body for %s: %v", op.ID, err))
	}
	return conformanceRequest{op: op, probe: probe, params: params, query: query, body: raw, contentType: "application/json"}
}

// send executes one request and applies the rules every response obeys.
func (s *conformanceSweep) send(ctx context.Context, req conformanceRequest) conformanceExchange {
	target := req.op.Path
	for name, value := range req.params {
		target = strings.ReplaceAll(target, "{"+name+"}", url.PathEscape(value))
	}
	if len(req.query) > 0 {
		target += "?" + req.query.Encode()
	}
	var body io.Reader
	switch {
	case req.stream != nil:
		body = req.stream
	case req.body != nil:
		body = bytes.NewReader(req.body)
	}
	reqCtx, cancel := context.WithTimeout(ctx, conformanceRequestBudget)
	defer cancel()
	r := httptest.NewRequestWithContext(reqCtx, req.op.Method, target, body)
	r.Header.Set("Authorization", api.TestAuthHeader())
	if req.announcedLength > 0 {
		r.ContentLength = req.announcedLength
	}
	if req.contentType != "" {
		r.Header.Set("Content-Type", req.contentType)
	}
	rec := &conformanceRecorder{ResponseRecorder: httptest.NewRecorder()}
	if req.op.Streams {
		// A stream ends when its client leaves; the first flush is the answer.
		rec.onFlush = cancel
	}
	s.handler.ServeHTTP(rec, r)
	ex := conformanceExchange{req: req, status: rec.Code, header: rec.Header().Clone(), body: bytes.Clone(rec.Body.Bytes())}
	if errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
		s.findings.add(ruleResponsesConform, req.op.ID, req.probe, "no answer within %s", conformanceRequestBudget)
	}
	s.observe(ctx, &ex)
	return ex
}

// sendAll sends requests one at a time, in order. Concurrent probes would
// race each other's mutation fences and make answers depend on scheduling.
func (s *conformanceSweep) sendAll(ctx context.Context, reqs []conformanceRequest) []conformanceExchange {
	out := make([]conformanceExchange, len(reqs))
	for i, req := range reqs {
		out[i] = s.send(ctx, req)
	}
	return out
}

// observe applies the rules every response obeys: it is declared and matches
// its schema, an error is the envelope with host copy, and no secret escapes.
func (s *conformanceSweep) observe(ctx context.Context, ex *conformanceExchange) {
	if ex.status >= 400 && strings.HasPrefix(ex.header.Get("Content-Type"), "application/json") {
		var apiErr conformanceAPIError
		dec := json.NewDecoder(bytes.NewReader(ex.body))
		dec.UseNumber()
		if err := dec.Decode(&apiErr); err == nil && apiErr.Code != "" {
			ex.apiErr = &apiErr
		}
	}
	s.checkResponse(ctx, *ex)
	s.checkErrorCopy(*ex)
	s.secrets.scanExchange(s.findings, *ex)
}

// conformanceRecorder cancels a stream on its first flush.
type conformanceRecorder struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (r *conformanceRecorder) Flush() {
	r.ResponseRecorder.Flush()
	if r.onFlush != nil {
		r.onFlush()
	}
}

// conformanceFindings collects violations by rule; safe for concurrent use.
type conformanceFindings struct {
	mu     sync.Mutex
	byRule map[conformanceRule]map[string]bool
	info   []string
}

func newConformanceFindings() *conformanceFindings {
	return &conformanceFindings{byRule: map[conformanceRule]map[string]bool{}}
}

func (f *conformanceFindings) add(rule conformanceRule, subject, probe, format string, args ...any) {
	line := subject + ": " + probe + ": " + fmt.Sprintf(format, args...)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byRule[rule] == nil {
		f.byRule[rule] = map[string]bool{}
	}
	f.byRule[rule][line] = true
}

// note records sweep coverage that is not a violation, such as a fixture the
// sweep could not create.
func (f *conformanceFindings) note(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info = append(f.info, fmt.Sprintf(format, args...))
}

func (f *conformanceFindings) list(rule conformanceRule) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.byRule[rule]))
	for line := range f.byRule[rule] {
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

func (f *conformanceFindings) notes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.info...)
}
