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
// answered, and every recorded event against its topic's schema. A contract
// change that the recording predates fails here, before the slower sweep that
// rewrites it runs.
func TestHostCorpusMatchesTheContract(t *testing.T) {
	t.Parallel()
	corpus, err := openapi.ReadHostCorpus()
	contractcheck.FailErr(t, "read "+openapi.HostCorpusPath+"; refresh with "+openapi.HostCorpusRefresh, err)
	validator, err := openapi.Bundled()
	contractcheck.FailErr(t, "load the bundled OpenAPI document", err)
	schemas, err := openapi.BundledEventSchemas()
	contractcheck.FailErr(t, "load the event schemas", err)

	for _, ex := range corpus.Exchanges {
		header := http.Header{"Content-Type": []string{ex.ContentType}}
		if err := validator.ValidateResponse(t.Context(), ex.Method, ex.Path, ex.Params, ex.Status, header, ex.Body); err != nil {
			line, _, _ := strings.Cut(err.Error(), "\n")
			t.Errorf("%s %d no longer matches the contract: %s", ex.Operation, ex.Status, line)
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
