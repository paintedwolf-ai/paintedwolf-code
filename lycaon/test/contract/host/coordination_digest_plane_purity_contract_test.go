package contract

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// TestCoordinationDigestPlanePurityContract keeps the two coordination planes
// distinct: findings (worker peer notes) and progress (coordinator plan) must not
// bleed fields into each other.
func TestCoordinationDigestPlanePurityContract(t *testing.T) {
	t.Parallel()
	findingsType := reflect.TypeOf(api.FindingsDigest{})
	for i := 0; i < findingsType.NumField(); i++ {
		field := findingsType.Field(i)
		switch field.Name {
		case "Steps", "Progress":
			t.Fatalf("FindingsDigest must not carry progress field %s — use ProgressDigest", field.Name)
		}
	}
	progressType := reflect.TypeOf(api.ProgressDigest{})
	for i := 0; i < progressType.NumField(); i++ {
		field := progressType.Field(i)
		if field.Name == "Findings" {
			t.Fatal("ProgressDigest must not carry findings — use FindingsDigest")
		}
	}
}
