//go:build darwin || linux

package hostos

import "testing"

func TestAccountIDsAreNumeric(t *testing.T) {
	if id, err := numericID("501"); err != nil || id != 501 {
		t.Fatalf("numeric id %d (%v)", id, err)
	}
	for _, value := range []string{"S-1-5-18", "-1", ""} {
		if _, err := numericID(value); err == nil {
			t.Fatalf("id %q accepted", value)
		}
	}
}
