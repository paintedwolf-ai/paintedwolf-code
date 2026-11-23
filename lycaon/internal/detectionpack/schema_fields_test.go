package detectionpack

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDetectionRuleSchemaFieldProjectionMatchesRuntime(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(testutil.CheckoutRoot(t), "schemas", "detection-rule.schema.json"))
	testutil.FailErr(t, "read detection rule schema", err)
	var doc struct {
		Fields map[string][]string `json:"x-paintedwolf-fields-by-service"`
	}
	testutil.FailErr(t, "parse detection rule schema", json.Unmarshal(raw, &doc))
	want := map[string][]string{
		string(SourceToolExec):       slices.Sorted(maps.Keys(fieldsForSource(SourceToolExec))),
		string(SourceEgressObserved): slices.Sorted(maps.Keys(fieldsForSource(SourceEgressObserved))),
	}
	for source, fields := range doc.Fields {
		sort.Strings(fields)
		doc.Fields[source] = fields
	}
	if !reflect.DeepEqual(doc.Fields, want) {
		t.Fatalf("schema fields=%v runtime=%v", doc.Fields, want)
	}
}
