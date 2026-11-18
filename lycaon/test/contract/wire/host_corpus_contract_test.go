package contract

import (
	"net/http"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

// TestHostCorpusMatchesTheContract keeps the corpus Den replays true to the
// current contract: every recorded answer validates against the operation it
// answered, every recorded event against its topic's schema, and every
// operation has a recorded success. A contract change that the recording
// predates fails here, before the slower sweep that rewrites it runs.
func TestHostCorpusMatchesTheContract(t *testing.T) {
	t.Parallel()
	corpus, err := openapi.ReadHostCorpus()
	contractcheck.FailErr(t, "read "+openapi.HostCorpusPath+"; refresh with "+openapi.HostCorpusRefresh, err)
	validator, err := openapi.Bundled()
	contractcheck.FailErr(t, "load the bundled OpenAPI document", err)
	doc, err := openapi.Document()
	contractcheck.FailErr(t, "load the bundled OpenAPI document", err)
	schemas, err := openapi.BundledEventSchemas()
	contractcheck.FailErr(t, "load the event schemas", err)

	succeeded := map[string]bool{}
	for _, ex := range corpus.Exchanges {
		header := http.Header{}
		switch {
		case len(ex.Body) > 0:
			header.Set("Content-Type", "application/json")
		case ex.Text != "":
			header.Set("Content-Type", "text/event-stream")
		}
		body := []byte(ex.Body)
		if ex.Text != "" {
			body = []byte(ex.Text)
		}
		if err := validator.ValidateResponse(t.Context(), ex.Method, ex.Path, ex.Params, ex.Status, header, body); err != nil {
			line, _, _ := strings.Cut(err.Error(), "\n")
			t.Errorf("%s %d no longer matches the contract: %s", ex.Operation, ex.Status, line)
		}
		if ex.Status/100 == 2 {
			succeeded[ex.Operation] = true
		}
	}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if !succeeded[op.OperationID] {
				t.Errorf("%s (%s %s) has no recorded success", op.OperationID, method, path)
			}
		}
	}
	for i, env := range corpus.Events {
		if err := schemas.Validate(env.Topic, env.Data); err != nil {
			t.Errorf("event %d (%s) no longer matches its schema: %s", i, env.Topic, openapi.SchemaErrorLine(err))
		}
	}
	if t.Failed() {
		t.Logf("refresh the corpus with %s, then make Den's replay pass", openapi.HostCorpusRefresh)
	}
}
