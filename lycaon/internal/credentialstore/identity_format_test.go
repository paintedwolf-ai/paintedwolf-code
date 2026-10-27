package credentialstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnknownIdentityFormatPreservesCustodianAndCiphertext(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "without ciphertext"
		if existing {
			name = "with valid ciphertext"
		}
		t.Run(name, func(t *testing.T) {
			items := &memoryItems{}
			path := filepath.Join(t.TempDir(), VaultBasename)
			var ciphertext []byte
			var before os.FileInfo
			if existing {
				path, ciphertext = custodiedVault(t, items)
				var err error
				before, err = os.Stat(path)
				testutil.FailErr(t, "inspect original vault", err)
			} else {
				provider := &itemsIdentityProvider{account: "test", items: items}
				_, err := provider.LoadOrCreate(false)
				testutil.FailErr(t, "create custodian identity", err)
			}
			valid := bytes.Clone(items.value)
			doc, err := decodeIdentityDocument(valid)
			testutil.FailErr(t, "decode valid identity", err)
			doc.Format = identityFormat + 1
			unknown, err := encodeIdentityDocument(doc)
			testutil.FailErr(t, "encode unknown identity format", err)
			items.value = bytes.Clone(unknown)
			creates, removes := items.creates, items.removes
			vault := &vaultFile{path: path, provider: &itemsIdentityProvider{account: "test", items: items}}
			backend := &namespaceBackend{vault: vault, namespace: "providers"}
			if err := backend.Set("openai", "must-not-replace"); !errors.Is(err, ErrVaultCorrupt) {
				t.Fatalf("Set with unknown identity format = %v, want corrupt vault", err)
			}
			if snapshot, err := backend.Snapshot(); !errors.Is(err, ErrVaultCorrupt) || len(snapshot.Values) != 0 {
				t.Fatalf("Snapshot with unknown identity format = %v, %v", snapshot.Values, err)
			}
			if vault.identity != nil || vault.vaultID != "" {
				t.Fatal("unknown identity was cached")
			}
			if !bytes.Equal(items.value, unknown) || items.creates != creates || items.removes != removes {
				t.Fatal("refusal replaced or removed the custodian identity")
			}
			current, err := os.ReadFile(path)
			if existing {
				testutil.FailErr(t, "read preserved vault", err)
				after, err := os.Stat(path)
				testutil.FailErr(t, "inspect preserved vault", err)
				if !bytes.Equal(current, ciphertext) || !os.SameFile(before, after) {
					t.Fatal("refusal changed or replaced the ciphertext")
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("refusal created ciphertext: %v", err)
			}
			items.value = valid
			snapshot, err := backend.Snapshot()
			testutil.FailErr(t, "read with restored supported identity", err)
			if existing && snapshot.Values["openai"] != "sk-kept" || !existing && len(snapshot.Values) != 0 {
				t.Fatalf("supported identity snapshot = %v", snapshot.Values)
			}
		})
	}
}
