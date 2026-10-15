package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func framed(password string) *bytes.Buffer {
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint32(len(password)))
	frame.WriteString(password)
	return &frame
}

func TestCustodyReportIsJSON(t *testing.T) {
	var out bytes.Buffer
	testutil.FailErr(t, "describe", describeCustody(&out, filepath.Join(t.TempDir(), credentialstore.VaultBasename)))
	var report credentialstore.CustodyReport
	testutil.FailErr(t, "decode", json.Unmarshal(out.Bytes(), &report))
	if report.Mode != credentialstore.CustodyDevelopment {
		t.Fatalf("report = %+v", report)
	}
}

func TestCustodyMovesNeedConfirmationAndReportStableCodes(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	t.Setenv(credentialstore.UnlockStdinEnv, "1")
	path := filepath.Join(t.TempDir(), credentialstore.VaultBasename)
	var moved string
	ok := func(p string) error { moved = p; return nil }

	if err := moveCustody("move-to-service", nil, framed("pw"), &bytes.Buffer{}, path, ok); err == nil || moved != "" {
		t.Fatal("a move ran without --yes")
	}
	if err := moveCustody("move-to-service", []string{"--bogus"}, framed("pw"), &bytes.Buffer{}, path, ok); err == nil {
		t.Fatal("an unknown flag was accepted")
	}
	if err := moveCustody("move-to-service", []string{"--yes"}, &bytes.Buffer{}, &bytes.Buffer{}, path, ok); err == nil || moved != "" {
		t.Fatal("a move ran without the framed password")
	}

	var out bytes.Buffer
	testutil.FailErr(t, "confirmed move", moveCustody("move-to-service", []string{"--yes"}, framed("correct horse battery"), &out, path, ok))
	var outcome credentialstore.CustodyOutcome
	testutil.FailErr(t, "decode outcome", json.Unmarshal(out.Bytes(), &outcome))
	if moved != path || outcome.Code != "ok" {
		t.Fatalf("moved %q, outcome %+v", moved, outcome)
	}

	out.Reset()
	wrong := func(string) error { return credentialstore.ErrVaultUnlockFailed }
	if err := moveCustody("move-to-app-password", []string{"--yes"}, framed("x"), &out, path, wrong); !errors.Is(err, credentialstore.ErrVaultUnlockFailed) {
		t.Fatalf("failed move: %v", err)
	}
	testutil.FailErr(t, "decode failure", json.Unmarshal(out.Bytes(), &outcome))
	if outcome.Code != "password_incorrect" || outcome.Message == "" {
		t.Fatalf("failure outcome = %+v", outcome)
	}
	if _, ok := custodyMoves["move-to-service"]; !ok {
		t.Fatal("move-to-service is not registered")
	}
	if err := runCredentials([]string{"custody", "extra"}); err == nil {
		t.Fatal("custody accepted an argument")
	}
	testutil.FailErr(t, "custody command", runCredentials([]string{"custody"}))
	if err := runCredentials([]string{"move-to-app-password"}); err == nil {
		t.Fatal("the move command ran without --yes")
	}
}
