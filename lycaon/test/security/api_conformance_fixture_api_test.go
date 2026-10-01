package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
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
  /v1/widgets/{id}:
    parameters: [{$ref: '#/components/parameters/WidgetID'}]
    get:
      operationId: getWidget
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Widget'}}}}
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
        '404': {$ref: '#/components/responses/Error'}
        '413': {$ref: '#/components/responses/Error'}
        '415': {$ref: '#/components/responses/Error'}
  /v1/widgets/{id}/parts/{part_id}:
    parameters:
      - {$ref: '#/components/parameters/WidgetID'}
      - {name: part_id, in: path, required: true, schema: {type: string, format: uuid}}
    get:
      operationId: getPart
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Part'}}}}
        '404': {$ref: '#/components/responses/Error'}
components:
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
      properties: {id: {type: string, format: uuid}, label: {type: string}}
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
}

// widgetAPI serves widgetSpec through the production responder.
type widgetAPI struct {
	faults    widgetFaults
	responses httpio.Responder
	writes    atomic.Int64
	mu        sync.Mutex
	token     string
}

func (a *widgetAPI) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/widgets", a.listWidgets)
	mux.HandleFunc("GET /v1/widgets/{id}", a.getWidget)
	mux.HandleFunc("POST /v1/widgets/{id}/parts", a.createPart)
	mux.HandleFunc("GET /v1/widgets/{id}/parts/{part_id}", a.getPart)
	return mux
}

var widgetPageLimit = httpio.MustPageLimit(20, 1, 50)

func (a *widgetAPI) listWidgets(w http.ResponseWriter, r *http.Request) {
	if !a.faults.clampsLimit {
		page, err := httpio.ReadPageQuery(r, widgetPageLimit)
		if err != nil {
			a.responses.InvalidQuery(w, err)
			return
		}
		if page.Cursor != "" {
			a.responses.PageCursorError(w, r, "cursor", pagecursor.ErrInvalid)
			return
		}
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]any{"widgets": []any{map[string]any{"id": fixtureWidgetID}}})
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
	if in.Name == "" {
		a.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "name"}, "")
		return
	}
	a.mu.Lock()
	a.token = in.Token
	a.mu.Unlock()
	httpio.WriteJSON(w, http.StatusCreated, map[string]any{"id": fixturePartID, "name": in.Name})
}

func (a *widgetAPI) getPart(w http.ResponseWriter, r *http.Request) {
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
type counterEffects struct{ writes *atomic.Int64 }

func (c counterEffects) mark(context.Context) (int64, error) { return c.writes.Load(), nil }

func (c counterEffects) since(_ context.Context, mark int64) ([]string, error) {
	if c.writes.Load() != mark {
		return []string{"store commit"}, nil
	}
	return nil, nil
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
			"invalid_json":           entry("Invalid JSON"),
			"unsupported_media_type": entry("Unsupported media type"),
			"body_too_large":         entry("Body too large"),
			"internal_error":         entry("Internal error"),
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
	sweep.effects = counterEffects{writes: &api.writes}
	sweep.values.bind("/v1/widgets/{id}/parts/{part_id}", "part_id", fixturePartID)
	return sweep
}
