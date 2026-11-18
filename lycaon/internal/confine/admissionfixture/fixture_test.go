package admissionfixture_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/admissionfixture"
)

func TestEnableRestoresProcessAdmission(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	if err := confine.RequireApplied(false); err != nil {
		t.Fatalf("fresh process requires confinement: %v", err)
	}
	t.Run("enabled", func(t *testing.T) {
		admissionfixture.Enable(t)
		t.Run("nested", func(t *testing.T) {
			admissionfixture.Enable(t)
			if err := confine.RequireApplied(false); !errors.Is(err, confine.ErrNotConfined) {
				t.Fatalf("nested scope did not require confinement: %v", err)
			}
		})
		if err := confine.RequireApplied(false); !errors.Is(err, confine.ErrNotConfined) {
			t.Fatalf("nested cleanup changed outer scope: %v", err)
		}
	})
	if err := confine.RequireApplied(false); err != nil {
		t.Fatalf("fixture did not restore default admission: %v", err)
	}
}
