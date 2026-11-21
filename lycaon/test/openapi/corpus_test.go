package openapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHostCorpusEventsMatchTheirSchemas(t *testing.T) {
	corpus, err := ReadHostCorpus()
	testutil.FailErr(t, "read host corpus", err)
	if len(corpus.Exchanges) == 0 || len(corpus.Events) == 0 {
		t.Fatalf("corpus holds %d exchanges and %d events", len(corpus.Exchanges), len(corpus.Events))
	}
	schemas, err := BundledEventSchemas()
	testutil.FailErr(t, "event schemas", err)
	for _, envelope := range corpus.Events {
		if err := schemas.Validate(envelope.Topic, envelope.Data); err != nil {
			t.Errorf("event %s (%s): %s", envelope.EventID, envelope.Topic, SchemaErrorLine(err))
		}
	}
}

func TestEventSchemasRefuseWhatTheyCannotCheck(t *testing.T) {
	schemas, err := BundledEventSchemas()
	testutil.FailErr(t, "event schemas", err)

	// A topic without a schema fails the same way on every use.
	for range 2 {
		if err := schemas.Validate(wire.EventTopic("no_such_topic"), json.RawMessage(`{}`)); err == nil {
			t.Fatal("a topic without a schema validated")
		}
	}

	corpus, err := ReadHostCorpus()
	testutil.FailErr(t, "read host corpus", err)
	topic := corpus.Events[0].Topic

	err = schemas.Validate(topic, json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("a non-JSON payload validated")
	}
	if line := SchemaErrorLine(err); strings.Contains(line, "\n") || line == "" {
		t.Fatalf("plain error line = %q", line)
	}

	err = schemas.Validate(topic, json.RawMessage(`[]`))
	if err == nil {
		t.Fatal("an array payload validated")
	}
	if line := SchemaErrorLine(err); !strings.HasPrefix(line, "/") || strings.Contains(line, "\n") {
		t.Fatalf("validation error line = %q", line)
	}
}

func TestValidateResponseChecksOnlyTheMediaTypeOfABinaryAnswer(t *testing.T) {
	zip := http.Header{"Content-Type": []string{"application/zip"}}
	err := ValidateResponse(context.Background(), http.MethodGet, "/v1/backup", nil, http.StatusOK, zip, nil)
	testutil.FailErr(t, "declared binary answer", err)

	text := http.Header{"Content-Type": []string{"text/plain"}}
	if err := ValidateResponse(context.Background(), http.MethodGet, "/v1/backup", nil, http.StatusOK, text, []byte("x")); err == nil {
		t.Fatal("an undeclared media type validated")
	}
}
