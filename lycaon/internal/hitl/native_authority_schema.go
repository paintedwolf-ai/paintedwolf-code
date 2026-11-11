package hitl

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/pkg/api"
)

var authorityTimeType = reflect.TypeFor[time.Time]()

func authorityKnownType(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		return authorityKnownType(t.Elem())
	}
	switch t {
	case reflect.TypeFor[socketref.Binding](), reflect.TypeFor[socketref.Enforcement](), reflect.TypeFor[string](), reflect.TypeFor[bool](), reflect.TypeFor[int](), reflect.TypeFor[uint16](), authorityTimeType,
		reflect.TypeFor[ApprovalOption](), reflect.TypeFor[ApprovalOptionKind](), reflect.TypeFor[ApprovalOptionRung](), reflect.TypeFor[ApprovalOptionDecision](),
		reflect.TypeFor[ApprovalGrantScope](), reflect.TypeFor[ApprovalAuthorityDelta](), reflect.TypeFor[AuthorityDeltaKind](),
		reflect.TypeFor[ApprovalGrant](), reflect.TypeFor[ApprovalGrantPredicate](), reflect.TypeFor[ApprovalGrantWitness](),
		reflect.TypeFor[ApprovalSocketTarget](), reflect.TypeFor[DirectIPLease](), reflect.TypeFor[GrantedPathDelta](), reflect.TypeFor[AskQuietDelta](), reflect.TypeFor[TrustDestinationDelta](),
		reflect.TypeFor[authzledger.PolicyIdentity](), reflect.TypeFor[secretmatch.Recipient](), reflect.TypeFor[secretmatch.ScreenSurface](), reflect.TypeFor[secretmatch.DestinationKind](), reflect.TypeFor[api.ElevatedAccessEffect]():
		return true
	default:
		return false
	}
}

func authorityFieldName(field reflect.StructField) (string, error) {
	if field.Anonymous {
		return "", fmt.Errorf("native authority has an unsupported embedded field")
	}
	if field.PkgPath != "" {
		return "", nil
	}
	tag := field.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", nil
	}
	if name == "" {
		name = field.Name
	}
	return name, nil
}

// Reflected sizes must fit the signed admission account.
func authorityStorageBytes(size uintptr) (int64, error) {
	if size > math.MaxInt64 {
		return 0, fmt.Errorf("native authority storage size overflow")
	}
	return int64(size), nil
}

func authorityOmitZero(field reflect.StructField) bool {
	for _, option := range strings.Split(field.Tag.Get("json"), ",")[1:] {
		if option == "omitzero" {
			return true
		}
	}
	return false
}
