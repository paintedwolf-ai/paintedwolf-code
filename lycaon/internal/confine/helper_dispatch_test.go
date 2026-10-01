package confine_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestHelperArgvNeverReachesTestBody(t *testing.T) {
	if confine.IsHelperInvocation(os.Args) {
		t.Fatal("helper argv reached the test body")
	}
}

func TestCommandRefusesNestedHelper(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"self", "__confine-exec", "--profile-fd", "3", "--", "/bin/echo"}
	_, _, err := confine.Command(context.Background(), "self", "/bin/echo", nil, confine.Confinement{
		Roots: []string{t.TempDir()},
	})
	if !errors.Is(err, confine.ErrNestedHelper) {
		t.Fatalf("nested helper wrap: %v", err)
	}
}
