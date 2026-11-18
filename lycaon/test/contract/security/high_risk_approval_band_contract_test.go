package contract

import (
	"go/build"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The consequence band is presentation attached after the gate decides: the
// deciding packages cannot reach the band derivation and the gate's result
// type carries no band.
func TestHighRiskApprovalBand(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	const bandPkg = "github.com/lycaon/lycaon/internal/approvals"
	for _, rel := range []string{"lycaon/internal/settings", "lycaon/internal/gate"} {
		pkg, err := build.ImportDir(filepath.Join(root, rel), 0)
		contractcheck.FailErr(t, "import "+rel, err)
		if len(pkg.Imports) == 0 {
			t.Fatalf("%s lists no imports", rel)
		}
		if slices.Contains(pkg.Imports, bandPkg) {
			t.Errorf("%s imports %s; the gate path must not derive the band", rel, bandPkg)
		}
	}

	bandTypes := map[reflect.Type]bool{
		reflect.TypeFor[api.ConsequenceBand](): true,
		reflect.TypeFor[api.ConsequenceCode](): true,
	}
	if path, ok := highRiskBandFieldPath(reflect.TypeFor[hitl.ApprovalResult](), bandTypes, map[reflect.Type]bool{}); ok {
		t.Fatalf("hitl.ApprovalResult carries a band field at %s", path)
	}
}

// highRiskBandFieldPath finds a field reachable from typ whose type is one of
// want, following pointers, slices, maps and nested structs.
func highRiskBandFieldPath(typ reflect.Type, want, seen map[reflect.Type]bool) (string, bool) {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
		typ = typ.Elem()
	}
	if want[typ] {
		return typ.String(), true
	}
	if typ.Kind() != reflect.Struct || seen[typ] {
		return "", false
	}
	seen[typ] = true
	for i := range typ.NumField() {
		field := typ.Field(i)
		if path, ok := highRiskBandFieldPath(field.Type, want, seen); ok {
			return typ.Name() + "." + field.Name + " -> " + path, true
		}
	}
	return "", false
}
