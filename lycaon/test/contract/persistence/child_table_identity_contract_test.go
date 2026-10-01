package contract

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestChildTablesNoProjectDirOnWire(t *testing.T) {
	t.Parallel()
	for _, typ := range []reflect.Type{
		reflect.TypeOf(api.WorkerTask{}),
		reflect.TypeOf(api.CreateBlueprintRequest{}),
		reflect.TypeOf(api.Blueprint{}),
		reflect.TypeOf(api.CreateDelegationRequest{}),
		reflect.TypeOf(api.Delegation{}),
	} {
		assertNoJSONField(t, typ, "ProjectDir", "project_dir")
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(api.WorkerTask{}),
		reflect.TypeOf(api.Blueprint{}),
		reflect.TypeOf(api.CreateDelegationRequest{}),
		reflect.TypeOf(api.Delegation{}),
	} {
		assertJSONField(t, typ, "ProjectID", "project_id")
	}
}

func assertJSONField(t *testing.T, typ reflect.Type, fieldName, wantTag string) {
	t.Helper()
	f, ok := typ.FieldByName(fieldName)
	if !ok {
		t.Fatalf("%s missing field %q", typ.Name(), fieldName)
	}
	if tag := strings.Split(f.Tag.Get("json"), ",")[0]; tag != wantTag {
		t.Fatalf("%s.%s json tag = %q want %q", typ.Name(), fieldName, tag, wantTag)
	}
}

func assertNoJSONField(t *testing.T, typ reflect.Type, fieldName, forbiddenTag string) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if strings.Split(f.Tag.Get("json"), ",")[0] == forbiddenTag {
			t.Fatalf("%s must not carry %s as json %q (found on %s)", typ.Name(), fieldName, forbiddenTag, f.Name)
		}
	}
}
