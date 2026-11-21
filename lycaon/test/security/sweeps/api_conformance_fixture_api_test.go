package sweeps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

// widgetSpec is a small API the sweep's own tests drive: a paged collection,
// a resource, a child collection taking a secret, and a child resource.
const widgetSpec = `
openapi: 3.1.0
info: {title: widgets, version: 1.0.0}
security: [{bearerAuth: []}]
paths:
  /v1/widgets:
    get:
      operationId: listWidgets
      parameters:
        - {name: cursor, in: query, schema: {type: string}}
        - {name: limit, in: query, schema: {type: integer, minimum: 1, maximum: 50}}
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/WidgetList'}}}}
        '400': {$ref: '#/components/responses/Error'}
        '401': {$ref: '#/components/responses/Error'}
  /v1/widgets/{id}:
    parameters: [{$ref: '#/components/parameters/WidgetID'}]
    get:
      operationId: getWidget
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Widget'}}}}
        '401': {$ref: '#/components/responses/Error'}
        '404': {$ref: '#/components/responses/Error'}
  /v1/widgets/{id}/parts:
    parameters: [{$ref: '#/components/parameters/WidgetID'}]
    post:
      operationId: createPart
      requestBody:
        required: true
        content: {application/json: {schema: {$ref: '#/components/schemas/PartInput'}}}
      responses:
        '201': {description: created, content: {application/json: {schema: {$ref: '#/components/schemas/Part'}}}}
        '400': {$ref: '#/components/responses/Error'}
        '401': {$ref: '#/components/responses/Error'}
        '404': {$ref: '#/components/responses/Error'}
        '413': {$ref: '#/components/responses/Error'}
        '415': {$ref: '#/components/responses/Error'}
  /v1/widgets/{id}/renames:
    parameters: [{$ref: '#/components/parameters/WidgetID'}]
    post:
      operationId: renameWidget
      requestBody:
        required: true
        content: {application/json: {schema: {$ref: '#/components/schemas/RenameInput'}}}
      responses:
        '200': {description: renamed, content: {application/json: {schema: {$ref: '#/components/schemas/Widget'}}}}
        '400': {$ref: '#/components/responses/Error'}
        '401': {$ref: '#/components/responses/Error'}
        '404': {$ref: '#/components/responses/Error'}
        '409': {$ref: '#/components/responses/Error'}
        '413': {$ref: '#/components/responses/Error'}
        '415': {$ref: '#/components/responses/Error'}
  /v1/widgets/{id}/parts/{part_id}:
    parameters:
      - {$ref: '#/components/parameters/WidgetID'}
      - {name: part_id, in: path, required: true, schema: {type: string, format: uuid}}
    get:
      operationId: getPart
      x-read-starts-work: widget_index
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Part'}}}}
        '401': {$ref: '#/components/responses/Error'}
        '404': {$ref: '#/components/responses/Error'}
components:
  securitySchemes:
    bearerAuth: {type: http, scheme: bearer}
  parameters:
    WidgetID: {name: id, in: path, required: true, schema: {type: string, format: uuid}}
  responses:
    Error: {description: error, content: {application/json: {schema: {$ref: '#/components/schemas/Error'}}}}
  schemas:
    Error:
      type: object
      required: [code, message]
      properties: {code: {type: string}, message: {type: string}, details: {type: object}}
    Widget:
      type: object
      required: [id]
      additionalProperties: false
      properties: {id: {type: string, format: uuid}, label: {type: [string, number]}}
    WidgetList:
      type: object
      required: [widgets]
      additionalProperties: false
      properties:
        widgets: {type: array, items: {$ref: '#/components/schemas/Widget'}}
        next_cursor: {type: string}
    Part:
      type: object
      required: [id, name]
      additionalProperties: false
      properties: {id: {type: string, format: uuid}, name: {type: string}}
    RenameInput:
      type: object
      required: [operation_id, label]
      additionalProperties: false
      properties:
        operation_id: {type: string, format: uuid}
        label: {type: string, minLength: 1}
    PartInput:
      type: object
      required: [name]
      additionalProperties: false
      properties:
        name: {type: string, minLength: 1}
        token: {type: string, writeOnly: true}
`

const (
	fixtureWidgetID = "7f1c2a4e-0000-4000-8000-000000000001"
	fixturePartID   = "7f1c2a4e-0000-4000-8000-000000000002"
	// fixtureSecondWidgetID is listed after fixtureWidgetID; the widget API
	// serves only the first by id.
	fixtureSecondWidgetID = "7f1c2a4e-0000-4000-8000-000000000003"
	// widgetSecondPage is the one cursor listWidgets issues.
	widgetSecondPage = "widgets-page-2"
)

// widgetFaults breaks one invariant each.
type widgetFaults struct {
	undeclaredStatus bool // a read answers a status it does not declare
	genericNotFound  bool // an unknown widget answers `not_found`
	childCodeWins    bool // an unknown widget under getPart answers the part's code
	lenientBodies    bool // bodies decode without media type, strictness, or bound
	clampsLimit      bool // an out-of-range limit is served
	writesOnRead     bool // a read changes host state
	echoesSecret     bool // a read reveals the stored token, base64-encoded
	rawMessage       bool // a 404 carries Go error text as its message
	undeclaredDetail bool // a 404 carries a details key its notice does not declare
	refusesCreate    bool // createPart refuses every valid body
	rowPerReplay     bool // renameWidget stores a row for every replay of a key
	acceptsReuse     bool // renameWidget applies a different body under a used key
	ignoresLimit     bool // listWidgets answers every row whatever the limit
	labelsWidget     bool // getWidget answers a string label
	numbersLabel     bool // getWidget answers its label as a number
	unguardedRoute   bool // getPart is registered without the bearer check
	writesPastEffect bool // getPart also writes widgets, beyond its declared index
	racesFirstUse    bool // renameWidget requests racing on a new key each store a row
}

// widgetAPI serves widgetSpec through the production responder.
type widgetAPI struct {
	faults    widgetFaults
	responses httpio.Responder
	writes    atomic.Int64
	// indexed counts writes to widget_index, the work getPart declares.
	indexed atomic.Int64
	mu      sync.Mutex
	token   string
	// renames holds one row per operation_id: the label it applied.
	renames []widgetRename
	// racing holds the requests on a key after the first that found it unstored.
	racing map[string]*widgetRace
}

type widgetRename struct{ key, label string }

func (a *widgetAPI) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/widgets", a.requireBearer(a.listWidgets))
	mux.HandleFunc("GET /v1/widgets/{id}", a.requireBearer(a.getWidget))
	mux.HandleFunc("POST /v1/widgets/{id}/parts", a.requireBearer(a.createPart))
	if a.faults.unguardedRoute {
		mux.HandleFunc("GET /v1/widgets/{id}/parts/{part_id}", a.getPart)
	} else {
		mux.HandleFunc("GET /v1/widgets/{id}/parts/{part_id}", a.requireBearer(a.getPart))
	}
	mux.HandleFunc("POST /v1/widgets/{id}/renames", a.requireBearer(a.renameWidget))
	return mux
}

// requireBearer admits only the test bearer, as the host's route seam does.
func (a *widgetAPI) requireBearer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if api.BearerTokenFromRequest(r) != api.TestAPIToken {
			a.responses.Fail(w, wire.ApiErrorCodeUnauthorized, "")
			return
		}
		next(w, r)
	}
}

var widgetPageLimit = httpio.MustPageLimit(20, 1, 50)

func (a *widgetAPI) listWidgets(w http.ResponseWriter, r *http.Request) {
	rows := []any{map[string]any{"id": fixtureWidgetID}, map[string]any{"id": fixtureSecondWidgetID}}
	page := httpio.PageQuery{Limit: len(rows)}
	if !a.faults.clampsLimit {
		var err error
		if page, err = httpio.ReadPageQuery(r, widgetPageLimit); err != nil {
			a.responses.InvalidQuery(w, err)
			return
		}
		if page.Cursor != "" && page.Cursor != widgetSecondPage {
			a.responses.PageCursorError(w, r, "cursor", pagecursor.ErrInvalid)
			return
		}
	}
	if page.Cursor == widgetSecondPage {
		rows = rows[1:]
	}
	answer := map[string]any{"widgets": rows}
	if page.Limit < len(rows) && !a.faults.ignoresLimit {
		answer["widgets"] = rows[:page.Limit]
		answer["next_cursor"] = widgetSecondPage
	}
	httpio.WriteJSON(w, http.StatusOK, answer)
}

func (a *widgetAPI) getWidget(w http.ResponseWriter, r *http.Request) {
	if a.faults.writesOnRead {
		a.writes.Add(1)
	}
	if r.PathValue("id") != fixtureWidgetID {
		a.widgetNotFound(w)
		return
	}
	if a.faults.undeclaredStatus {
		a.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "")
		return
	}
	widget := map[string]any{"id": fixtureWidgetID}
	if a.faults.labelsWidget {
		widget["label"] = "relabelled"
	}
	if a.faults.numbersLabel {
		widget["label"] = 7
	}
	if a.faults.echoesSecret {
		a.mu.Lock()
		widget["label"] = base64.StdEncoding.EncodeToString([]byte("token=" + a.token))
		a.mu.Unlock()
	}
	httpio.WriteJSON(w, http.StatusOK, widget)
}

func (a *widgetAPI) widgetNotFound(w http.ResponseWriter) {
	switch {
	case a.faults.genericNotFound:
		a.responses.Fail(w, wire.ApiErrorCodeNotFound, "")
	case a.faults.rawMessage:
		httpio.WriteJSON(w, http.StatusNotFound, wire.ErrorResponse{Code: wire.ApiErrorCodeSessionNotFound, Message: "sql: no rows in result set"})
	case a.faults.undeclaredDetail:
		a.responses.FailDetails(w, wire.ApiErrorCodeSessionNotFound, map[string]any{"path": "/private/var/widgets.db"}, "")
	default:
		a.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "")
	}
}

func (a *widgetAPI) createPart(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != fixtureWidgetID {
		a.widgetNotFound(w)
		return
	}
	var in struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if a.faults.lenientBodies {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	} else if err := httpio.DecodeJSON(w, r, &in); err != nil {
		a.responses.DecodeError(w, r, err)
		return
	}
	if in.Name == "" || a.faults.refusesCreate {
		a.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "name"}, "")
		return
	}
	a.mu.Lock()
	a.token = in.Token
	a.mu.Unlock()
	httpio.WriteJSON(w, http.StatusCreated, map[string]any{"id": fixturePartID, "name": in.Name})
}

func (a *widgetAPI) renameWidget(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != fixtureWidgetID {
		a.widgetNotFound(w)
		return
	}
	var in struct {
		OperationID string `json:"operation_id"`
		Label       string `json:"label"`
	}
	if err := httpio.DecodeJSON(w, r, &in); err != nil {
		a.responses.DecodeError(w, r, err)
		return
	}
	if in.OperationID == "" || in.Label == "" {
		a.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "label"}, "")
		return
	}
	if a.faults.racesFirstUse && a.raceFirstUse(in.OperationID) {
		a.mu.Lock()
		a.renames = append(a.renames, widgetRename{key: in.OperationID, label: in.Label})
		a.mu.Unlock()
		httpio.WriteJSON(w, http.StatusOK, map[string]any{"id": fixtureWidgetID, "label": in.Label})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, done := range a.renames {
		if done.key != in.OperationID || a.faults.acceptsReuse || a.faults.rowPerReplay {
			continue
		}
		if done.label != in.Label {
			a.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "")
			return
		}
		httpio.WriteJSON(w, http.StatusOK, map[string]any{"id": fixtureWidgetID, "label": done.label})
		return
	}
	a.renames = append(a.renames, widgetRename{key: in.OperationID, label: in.Label})
	httpio.WriteJSON(w, http.StatusOK, map[string]any{"id": fixtureWidgetID, "label": in.Label})
}

// widgetRace gathers the requests that found one key unstored.
type widgetRace struct {
	arrived int
	all     chan struct{}
}

// raceFirstUse reports whether a request on key races: a key other than the
// first one stored is checked by every racing request before any stores it,
// so each request waits for the others and then stores its own row.
func (a *widgetAPI) raceFirstUse(key string) bool {
	a.mu.Lock()
	if len(a.renames) == 0 || slices.ContainsFunc(a.renames, func(done widgetRename) bool { return done.key == key }) {
		a.mu.Unlock()
		return false
	}
	if a.racing == nil {
		a.racing = map[string]*widgetRace{}
	}
	race := a.racing[key]
	if race == nil {
		race = &widgetRace{all: make(chan struct{})}
		a.racing[key] = race
	}
	race.arrived++
	if race.arrived == idempotentReplays {
		close(race.all)
	}
	a.mu.Unlock()
	<-race.all
	return true
}

// keyRows counts the renames stored under key.
func (a *widgetAPI) keyRows(_ context.Context, key string) (map[string]int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	counts := map[string]int{}
	for _, done := range a.renames {
		if done.key == key {
			counts["renames"]++
		}
	}
	return counts, nil
}

func (a *widgetAPI) getPart(w http.ResponseWriter, r *http.Request) {
	// Reading a part indexes it, the work the operation declares.
	a.indexed.Add(1)
	if a.faults.writesPastEffect {
		a.writes.Add(1)
	}
	unknownWidget := r.PathValue("id") != fixtureWidgetID
	unknownPart := r.PathValue("part_id") != fixturePartID
	switch {
	case a.faults.childCodeWins && (unknownWidget || unknownPart):
		// Looks the part up within the widget and reports only the part.
		a.responses.Fail(w, wire.ApiErrorCodeSourcePinNotFound, "")
	case unknownWidget:
		a.widgetNotFound(w)
	case unknownPart:
		a.responses.Fail(w, wire.ApiErrorCodeSourcePinNotFound, "")
	default:
		httpio.WriteJSON(w, http.StatusOK, map[string]any{"id": fixturePartID, "name": "part"})
	}
}

// counterEffects reports a change whenever the widget API counted a write.
type counterEffects struct{ writes, indexed *atomic.Int64 }

func (c counterEffects) mark(context.Context) (int64, error) {
	return c.writes.Load() + c.indexed.Load(), nil
}

func (c counterEffects) since(ctx context.Context, mark int64) ([]string, error) {
	if now, _ := c.mark(ctx); now != mark {
		return []string{"store commit"}, nil
	}
	return nil, nil
}

func (c counterEffects) tables(context.Context) (map[string]string, error) {
	return map[string]string{
		"widgets":      strconv.FormatInt(c.writes.Load(), 10),
		"widget_index": strconv.FormatInt(c.indexed.Load(), 10),
	}, nil
}

// widgetNotices is the fixture catalog: every code the widget API answers,
// with the render variables its responders pass.
func widgetNotices(t *testing.T) *usernotice.Catalog {
	t.Helper()
	entry := func(title string, context ...any) usernotice.Entry {
		e := usernotice.Entry{
			Surfaces: []string{"http"}, Title: title, Message: title + ".", SuggestedAction: "Try again.",
			Notification: &usernotice.Notification{Tier: usernotice.TierNonCatastrophic, Scope: usernotice.ScopeApp},
		}
		if len(context) > 0 {
			e.ContextSchema = map[string]any{"optional": context}
		}
		return e
	}
	cfg, err := usernotice.ConfigFromParts(
		usernotice.NoticeCopy{Title: "Request failed", Message: "The request failed.", SuggestedAction: "Try again."},
		map[string]usernotice.Entry{
			"not_found":              entry("Not found"),
			"session_not_found":      entry("Widget not found"),
			"source_pin_not_found":   entry("Part not found"),
			"invalid_request":        entry("Invalid request", "field", "reason"),
			"invalid_query":          entry("Invalid query", "param", "reason"),
			"invalid_page_cursor":    entry("Invalid cursor", "param"),
			"idempotency_conflict":   entry("Operation reused"),
			"invalid_json":           entry("Invalid JSON"),
			"unsupported_media_type": entry("Unsupported media type"),
			"body_too_large":         entry("Body too large"),
			"internal_error":         entry("Internal error"),
			"unauthorized":           entry("Not signed in"),
		})
	testutil.FailErr(t, "build the fixture notice catalog", err)
	return usernotice.NewCatalog(cfg)
}

// newWidgetSweep builds a sweep of widgetSpec against a widget API with faults.
func newWidgetSweep(t *testing.T, faults widgetFaults) *conformanceSweep {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData([]byte(widgetSpec))
	testutil.FailErr(t, "load the widget spec", err)
	validator, err := openapi.NewValidator(doc)
	testutil.FailErr(t, "validate the widget spec", err)
	notices := widgetNotices(t)
	api := &widgetAPI{faults: faults, responses: httpio.Responder{Logger: slog.New(slog.DiscardHandler), Notices: notices}}
	sweep := newConformanceSweep(doc, validator, api.routes(), notices)
	sweep.effects = counterEffects{writes: &api.writes, indexed: &api.indexed}
	sweep.readEffects = map[string]readEffect{"widget_index": {tables: []string{"widget_index"}}}
	sweep.firstUseOp = "renameWidget"
	sweep.corpus = newCorpusRecorder("getWidget")
	sweep.keyRows = api
	sweep.eventSchemas = openapi.NewEventSchemas(widgetEventSchemaDir(t))
	sweep.values.bind("/v1/widgets/{id}/parts/{part_id}", "part_id", fixturePartID)
	return sweep
}

// widgetEventSchemaDir lays out a docs tree whose project topic schema
// references a YAML component file, as docs/schemas/events does.
func widgetEventSchemaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"schemas/events/project.json": `{"$id": "https://paintedwolf.dev/schemas/events/project.json",
  "$ref": "../../openapi/components/schemas/widgets.yaml#/components/schemas/WidgetEvent"}`,
		"openapi/components/schemas/widgets.yaml": `components:
  schemas:
    WidgetEvent:
      type: object
      required: [widget_id]
      additionalProperties: false
      properties:
        widget_id: {type: string, format: uuid}
`,
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		testutil.FailErr(t, "create "+name, os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(path, []byte(body), 0o644))
	}
	return dir
}
