package sourceref

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMetadataRoundTripAndBaseline(t *testing.T) {
	context := &api.SourceContext{Locations: []api.NavigationTarget{location(".hidden/a.go")}}
	raw, err := EncodeMetadata(nil, context)
	testutil.FailErr(t, "encode source context", err)
	got, err := DecodeMetadata(raw)
	testutil.FailErr(t, "decode source context", err)
	if !reflect.DeepEqual(got.Context, *context) || got.Refs == nil {
		t.Fatalf("metadata=%+v", got)
	}
	for _, malformed := range []string{`{"v":1,"refs":[]}`, `{"v":1,"refs":[],"context":{"locations":[{"path":"../escape"}]}}`} {
		if _, err := DecodeMetadata(sql.NullString{String: malformed, Valid: true}); err == nil {
			t.Fatalf("accepted malformed metadata: %s", malformed)
		}
	}
}
