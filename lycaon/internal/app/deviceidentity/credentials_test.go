package deviceidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialsPersistHostIdentityAndKeepExplicitToken(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, directory)
	t.Setenv("LYCAON_API_TOKEN", " explicit-token ")
	first, err := Load("")
	testutil.FailErr(t, "load host credentials", err)
	second, err := Load("")
	testutil.FailErr(t, "reload host credentials", err)
	if first.Token != "explicit-token" || first.Generated || first.Host.HostID != second.Host.HostID || first.Host.EncodedPublicKey() != second.Host.EncodedPublicKey() {
		t.Fatalf("credentials changed across reload: %+v / %+v", first, second)
	}
	legacyPath := filepath.Join(directory, api.LegacyTokenFileName)
	testutil.FailErr(t, "seed legacy api.token", os.WriteFile(legacyPath, []byte("legacy-token\n"), 0o600))
	t.Setenv("LYCAON_API_TOKEN", "")
	generated, err := Load("")
	testutil.FailErr(t, "generate token", err)
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy api.token was not unlinked after generated token persistence: %v", err)
	}
	if stored, err := api.ReadAPIToken(); err != nil || stored != generated.Token {
		t.Fatalf("stored vault token = %q (%v), want %q", stored, err, generated.Token)
	}
	again, err := Load("")
	testutil.FailErr(t, "reload generated token", err)
	if !generated.Generated || generated.Token == "" || generated.Token == again.Token || generated.Host.HostID != first.Host.HostID || generated.Host.EncodedPublicKey() != first.Host.EncodedPublicKey() {
		t.Fatal("startup token was reused or persistent host identity changed")
	}
	if stored, err := api.ReadAPIToken(); err != nil || stored != again.Token {
		t.Fatalf("updated vault token = %q (%v), want %q", stored, err, again.Token)
	}
}

func TestCredentialsPreferTheDeliveredToken(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	t.Setenv("LYCAON_API_TOKEN", "environment-token")
	credentials, err := Load(" delivered-token ")
	testutil.FailErr(t, "load with a delivered token", err)
	if credentials.Token != "delivered-token" || credentials.Generated {
		t.Fatalf("delivered token not preferred: %+v", credentials)
	}
}

func TestCredentialsRefuseUnwritableConfigurationWithoutInventingIdentity(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "config")
	testutil.FailErr(t, "block config directory", os.WriteFile(blocked, []byte("occupied"), 0600))
	t.Setenv(configdir.EnvConfigDir, blocked)
	t.Setenv("LYCAON_API_TOKEN", "explicit-token")
	if got, err := Load(""); err == nil || got.Token != "" {
		t.Fatalf("identity load returned credentials despite blocked config: %+v, %v", got, err)
	}
	t.Setenv("LYCAON_API_TOKEN", "")
	if token, generated, err := ResolveToken(""); err != nil || token == "" || !generated {
		t.Fatalf("ephemeral token generation depended on config storage: %q,%v,%v", token, generated, err)
	}
}
