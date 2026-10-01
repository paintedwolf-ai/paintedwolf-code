package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryConstructionFailsWhenMandatoryOAuthStoreCannotOpen(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	testutil.FailErr(t, "write blocking file", os.WriteFile(blocked, []byte("x"), 0o600))
	t.Setenv(configdir.EnvConfigDir, blocked)

	_, err := NewRegistryImpl(RegistryOptions{})
	if err == nil || !strings.Contains(err.Error(), "mcp oauth credentials:") {
		t.Fatalf("NewRegistryImpl error = %v", err)
	}
}
