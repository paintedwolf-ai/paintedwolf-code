package openapi

import (
	"encoding/json"
	"os"
	"path/filepath"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// HostCorpusPath is the committed record of what the host answers and
// publishes, relative to the repository root. Den replays it through its
// client and stores, so refreshing it changes a Den path.
const HostCorpusPath = "lycaon-den/src/api/testdata/host-corpus.json"

// HostCorpusRefresh is the command that rewrites the corpus.
const HostCorpusRefresh = "UPDATE_HOST_CORPUS=1 ./task test:digest -- ./test/security/sweeps -run TestAPIConformance"

// HostCorpus is what the conformance sweep observed the host answer and
// publish.
type HostCorpus struct {
	Generated string `json:"generated"`
	// Exchanges holds one chat's bootstrap before and after the events that
	// change it.
	Exchanges []CorpusExchange `json:"exchanges"`
	// Events holds, in delivery order, the bootstraps' cursor boundaries, the
	// chat's message events between them, and the first envelope of each topic
	// and payload shape.
	Events []wire.EventEnvelope `json:"events"`
}

// CorpusExchange is one request and the host's answer.
type CorpusExchange struct {
	Operation string              `json:"operation"`
	Method    string              `json:"method"`
	Path      string              `json:"path"`
	Params    map[string]string   `json:"params,omitempty"`
	Query     map[string][]string `json:"query,omitempty"`
	Request   json.RawMessage     `json:"request,omitempty"`
	Status    int                 `json:"status"`
	// ContentType is the answer's media type.
	ContentType string `json:"content_type,omitempty"`
	// Body is the JSON answer.
	Body json.RawMessage `json:"body,omitempty"`
}

// ReadHostCorpus loads the committed corpus.
func ReadHostCorpus() (HostCorpus, error) {
	root, err := RepoRoot()
	if err != nil {
		return HostCorpus{}, err
	}
	raw, err := os.ReadFile(filepath.Join(root, HostCorpusPath))
	if err != nil {
		return HostCorpus{}, err
	}
	var corpus HostCorpus
	err = json.Unmarshal(raw, &corpus)
	return corpus, err
}
