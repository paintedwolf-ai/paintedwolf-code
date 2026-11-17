package contract

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// The log digest ships inside read tool-result JSON rather than a /v1/* response,
// so it has no HTTP route to conform-check; field parity with OpenAPI and Den is
// checked here instead.
func TestLogOutlineWireTypesRegistered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, spec := range []wirespec.DtoSyncSpec{
		{GoValue: api.LogDigest{}, OpenAPISchema: "LogDigest", TSInterface: "LogDigest"},
		{GoValue: api.ReadOutlineResponse{}, OpenAPISchema: "ReadOutlineResponse", TSInterface: "ReadOutlineResponse"},
	} {
		if err := wirespec.SyncDTOFields(root, spec); err != nil {
			t.Fatalf("%s: %v", spec.OpenAPISchema, err)
		}
	}
}

// Every format the outline can report is a wire LogFormat value, and back.
func TestLogFormatInternalMatchesWire(t *testing.T) {
	t.Parallel()
	enums, err := wirespec.DiscoverAPIStringEnums(contractcheck.RepoRoot(t))
	contractcheck.FailErr(t, "discover API string enums", err)
	if len(enums["LogFormat"]) == 0 {
		t.Fatal("wire LogFormat enum not found")
	}
	var internal []string
	for _, format := range logoutline.Formats() {
		internal = append(internal, string(format))
	}
	contractcheck.FailSetEqual(t, "logoutline formats vs wire LogFormat", internal, enums["LogFormat"])
}

func TestLogDigestInternalJSONMatchesWire(t *testing.T) {
	t.Parallel()
	assertSameJSONFields(t, "Digest", reflect.TypeOf(logoutline.Digest{}), reflect.TypeOf(api.LogDigest{}))
	assertSameJSONFields(t, "TimeSpan", reflect.TypeOf(logoutline.TimeSpan{}), reflect.TypeOf(api.LogDigestTimeSpan{}))
	assertSameJSONFields(t, "FieldStat", reflect.TypeOf(logoutline.FieldStat{}), reflect.TypeOf(api.LogDigestFieldStat{}))
	assertSameJSONFields(t, "Facet", reflect.TypeOf(logoutline.Facet{}), reflect.TypeOf(api.LogDigestFacet{}))
	assertSameJSONFields(t, "FacetValue", reflect.TypeOf(logoutline.FacetValue{}), reflect.TypeOf(api.LogDigestFacetValue{}))
	assertSameJSONFields(t, "Cluster", reflect.TypeOf(logoutline.Cluster{}), reflect.TypeOf(api.LogDigestCluster{}))
}

func assertSameJSONFields(t *testing.T, label string, internal, wire reflect.Type) {
	t.Helper()
	internalFields := wirespec.JsonFieldNames(internal)
	wireFields := wirespec.JsonFieldNames(wire)
	if !contractcheck.SortedSetEqual(internalFields, wireFields) {
		t.Fatalf("%s JSON fields drift:\ninternal: %v\nwire: %v", label, internalFields, wireFields)
	}
}
