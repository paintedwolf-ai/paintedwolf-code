package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

func TestStoredIdentityDamageIsDistinctFromMissingAndIOFailure(t *testing.T) {
	for _, tt := range []struct {
		name               string
		value              []byte
		status             vaultidentity.Status
		missing, directory bool
	}{
		{name: "empty", value: []byte{}, status: vaultidentity.StatusCorrupt},
		{name: "oversized", value: bytes.Repeat([]byte("x"), vaultidentity.MaxValueBytes+1), status: vaultidentity.StatusCorrupt},
		{name: "missing", missing: true, status: vaultidentity.StatusNotFound},
		{name: "read I/O", directory: true, status: vaultidentity.StatusUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			const account = "age-x25519-identity-v1:0a"
			path := f.server.Store.path(f.peer.UID, account)
			testutil.FailErr(t, "make user directory", os.MkdirAll(filepath.Dir(path), 0o700))
			if tt.directory {
				testutil.FailErr(t, "make unreadable identity object", os.Mkdir(path, 0o700))
			} else if !tt.missing {
				testutil.FailErr(t, "write damaged identity", os.WriteFile(path, tt.value, 0o600))
			}
			before, statErr := os.Lstat(path)
			if tt.missing {
				if !os.IsNotExist(statErr) {
					t.Fatalf("missing fixture: %v", statErr)
				}
			} else {
				testutil.FailErr(t, "stat identity before reads", statErr)
			}
			_, err := f.server.Store.Read(f.peer.UID, account)
			if err == nil || errors.Is(err, errStoredIdentityCorrupt) != (tt.status == vaultidentity.StatusCorrupt) ||
				errors.Is(err, errNotFound) != tt.missing {
				t.Fatalf("stored identity classification: %v", err)
			}
			client := f.start()
			if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpProbe}); resp.Status != vaultidentity.StatusOK {
				t.Fatalf("healthy service probe: %+v", resp)
			}
			for range 2 {
				resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: account})
				if resp.Status != tt.status || len(resp.Value) != 0 {
					t.Fatalf("stored identity response = %+v, want %s without identity bytes", resp, tt.status)
				}
			}
			after, err := os.Lstat(path)
			if tt.missing {
				if !os.IsNotExist(err) {
					t.Fatalf("read recreated a missing identity: %v", err)
				}
			} else {
				testutil.FailErr(t, "stat retained identity", err)
				if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
					t.Fatal("read replaced or changed the stored identity object")
				}
				if !tt.directory {
					raw, err := os.ReadFile(path)
					testutil.FailErr(t, "read retained identity bytes", err)
					if !bytes.Equal(raw, tt.value) {
						t.Fatal("read mutated damaged identity bytes")
					}
				}
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			testutil.FailErr(t, "list retained identity directory", err)
			want := 1
			if tt.missing {
				want = 0
			}
			if len(entries) != want {
				t.Fatal("read created an extra identity or staging file")
			}
			if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpProbe}); resp.Status != vaultidentity.StatusOK {
				t.Fatalf("one damaged identity disabled the custodian: %+v", resp)
			}
		})
	}
}
