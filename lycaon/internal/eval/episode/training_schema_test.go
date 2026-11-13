package episode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The factory validates every bundle against these published schemas, so the
// host's records and the schemas must name the same fields and vocabularies.
const contractsDir = "../../../../scripts/bialy/custom_model/contracts"

type schemaDoc struct {
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
	Defs       map[string]schemaDoc       `json:"$defs"`
}

func readSchema(t *testing.T, name string) schemaDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractsDir, name+".schema.json"))
	testutil.FailErr(t, "read published schema", err)
	var doc schemaDoc
	testutil.FailErr(t, "decode published schema", json.Unmarshal(raw, &doc))
	return doc
}

func jsonKeys(t *testing.T, value any) []string {
	t.Helper()
	raw, err := json.Marshal(value)
	testutil.FailErr(t, "encode host record", err)
	var fields map[string]json.RawMessage
	testutil.FailErr(t, "decode host record", json.Unmarshal(raw, &fields))
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func sorted(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}

// constOf returns a property's fixed value in compact form.
func constOf(t *testing.T, doc schemaDoc, field string) string {
	t.Helper()
	var property struct {
		Const any `json:"const"`
	}
	testutil.FailErr(t, "decode schema property", json.Unmarshal(doc.Properties[field], &property))
	raw, err := json.Marshal(property.Const)
	testutil.FailErr(t, "encode schema constant", err)
	return string(raw)
}

func TestTheContractAndBundleRecordsFollowTheirPublishedSchemas(t *testing.T) {
	contract := readSchema(t, "training-contract")
	if !slices.Equal(sorted(contract.Required), jsonKeys(t, Contract())) {
		t.Fatalf("contract fields = %v, schema requires %v", jsonKeys(t, Contract()), sorted(contract.Required))
	}
	for field, value := range map[string]any{
		"files": BundleFiles, "settlement": Contract().Settlement,
		"semantic_outcome": Contract().SemanticOutcome, "effect_state": Contract().EffectState,
	} {
		raw, err := json.Marshal(value)
		testutil.FailErr(t, "encode vocabulary", err)
		if string(raw) != constOf(t, contract, field) {
			t.Fatalf("%s = %s, schema fixes %s", field, raw, constOf(t, contract, field))
		}
	}
	bundle := readSchema(t, "episode-bundle")
	manifest := BundleManifest{Files: map[string]string{}, Blobs: []string{}}
	if !slices.Equal(sorted(bundle.Required), jsonKeys(t, manifest)) {
		t.Fatalf("manifest fields = %v, schema requires %v", jsonKeys(t, manifest), sorted(bundle.Required))
	}
	settled := time.Now()
	full := Operation{ParentID: "p", MessageID: "m", ToolCallID: "c", Tool: "t", ArgsDigest: "a", FailureCode: "f", SourceVerdict: "passed", SourceRevision: "r", SettledAt: &settled}
	keys := jsonKeys(t, full)
	definition := bundle.Defs["operation"]
	for _, key := range keys {
		if _, ok := definition.Properties[key]; !ok {
			t.Fatalf("operation field %q is not in the published schema", key)
		}
	}
	for _, key := range definition.Required {
		if !slices.Contains(jsonKeys(t, Operation{}), key) {
			t.Fatalf("schema requires %q, which an operation can omit", key)
		}
	}
}
