package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/startupprotocol"
)

func TestReadStartupAPITokenFromStdin(t *testing.T) {
	var cfg configuration.Config
	if err := readStartupAPIToken(strings.NewReader(""), &cfg); err != nil || cfg.APIToken != "" {
		t.Fatalf("unset env: got token=%q err=%v", cfg.APIToken, err)
	}
	t.Setenv(startupprotocol.APITokenStdinEnv, "1")
	want := strings.Repeat("a", 43)
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint32(len(want)))
	frame.WriteString(want)
	if err := readStartupAPIToken(&frame, &cfg); err != nil || cfg.APIToken != want {
		t.Fatalf("valid frame: got token=%q err=%v", cfg.APIToken, err)
	}
	if err := readStartupAPIToken(strings.NewReader("bad"), &cfg); err == nil {
		t.Fatal("expected malformed token frame to fail")
	}
}

func TestStartupFailureSeparatesCustodyFromCorruption(t *testing.T) {
	for err, want := range map[error]string{
		hostlock.ErrStoreInstanceLocked:         "store_locked",
		credentialstore.ErrVaultLocked:          "credential_vault_locked",
		credentialstore.ErrVaultUninitialized:   "credential_vault_uninitialized",
		credentialstore.ErrVaultUnlockFailed:    "credential_vault_unlock_failed",
		credentialstore.ErrCustodianUnavailable: "credential_vault_custodian_unavailable",
		credentialstore.ErrCustodianAbsent:      "credential_vault_custodian_absent",
		credentialstore.ErrLauncherRefused:      "credential_vault_launcher_refused",
		credentialstore.ErrIdentityMissing:      "credential_vault_identity_missing",
		credentialstore.ErrVaultCorrupt:         "credential_vault_corrupt",
		errors.New("unclassified"):              "build_failed",
	} {
		if got := startupFailureCode(fmt.Errorf("initialize credentials: %w", err)); got != want {
			t.Errorf("%v: got %q, want %q", err, got, want)
		}
	}
}
