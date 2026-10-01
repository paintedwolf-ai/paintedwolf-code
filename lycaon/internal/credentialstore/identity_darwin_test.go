package credentialstore

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type memoryKeychain struct {
	value                         []byte
	readErr, createErr, removeErr error
	creates, removes              int
}

func (m *memoryKeychain) read(string) ([]byte, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	if m.value == nil {
		return nil, keychainNotFound
	}
	return append([]byte(nil), m.value...), nil
}
func (m *memoryKeychain) create(_ string, value []byte) error {
	m.creates++
	if m.createErr != nil {
		return m.createErr
	}
	m.value = append([]byte(nil), value...)
	return nil
}
func (m *memoryKeychain) remove(string) error {
	m.removes++
	if m.removeErr != nil {
		return m.removeErr
	}
	m.value = nil
	return nil
}

func TestKeychainIdentityCreatesAndReloads(t *testing.T) {
	items := &memoryKeychain{}
	p := &keychainIdentityProvider{account: "test", items: items}
	created, err := p.LoadOrCreate(false)
	testutil.FailErr(t, "create identity", err)
	loaded, err := p.LoadOrCreate(true)
	testutil.FailErr(t, "load identity", err)
	if loaded != created || items.creates != 1 {
		t.Fatal("identity was not reused")
	}
	testutil.FailErr(t, "purge identity", p.Purge())
	if items.value != nil || items.removes != 1 {
		t.Fatal("purge did not remove the item")
	}
}

func TestKeychainIdentityFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name    string
		exists  bool
		value   []byte
		readErr error
	}{
		{name: "missing under vault", exists: true},
		{name: "empty item", value: []byte{}},
		{name: "damaged item", value: []byte("{")},
		{name: "locked", readErr: keychainStatus(-25308)},
		{name: "missing entitlement", readErr: keychainMissingEntitlement},
		{name: "wrong protection attributes", readErr: keychainStatus(-26275)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			items := &memoryKeychain{value: tt.value, readErr: tt.readErr}
			p := &keychainIdentityProvider{account: "test", items: items}
			if _, err := p.LoadOrCreate(tt.exists); err == nil {
				t.Fatal("unusable identity accepted")
			}
			if items.creates != 0 || items.removes != 0 {
				t.Fatal("unusable identity was modified")
			}
		})
	}
}

func TestKeychainIdentityDoesNotFallbackOnCreateFailure(t *testing.T) {
	items := &memoryKeychain{createErr: keychainMissingEntitlement}
	p := &keychainIdentityProvider{account: "test", items: items}
	if _, err := p.LoadOrCreate(false); !errors.Is(err, items.createErr) {
		t.Fatalf("create error = %v", err)
	}
}

func TestKeychainIdentityAccountsAreIsolated(t *testing.T) {
	a := newKeychainIdentityProvider("/a/credential-vault.age").(*keychainIdentityProvider)
	same := newKeychainIdentityProvider("/a/./credential-vault.age").(*keychainIdentityProvider)
	b := newKeychainIdentityProvider("/b/credential-vault.age").(*keychainIdentityProvider)
	if a.account != same.account || a.account == b.account {
		t.Fatal("vault identity namespace is not stable and isolated")
	}
}

type competingKeychain struct {
	memoryKeychain
	winner []byte
}

func (m *competingKeychain) create(string, []byte) error {
	m.value = m.winner
	return keychainDuplicate
}
func TestKeychainConcurrentCreatorUsesStoredWinner(t *testing.T) {
	winner, err := generateIdentityDocument()
	testutil.FailErr(t, "generate winner", err)
	raw, err := encodeIdentityDocument(winner)
	testutil.FailErr(t, "encode winner", err)
	p := &keychainIdentityProvider{account: "test", items: &competingKeychain{winner: raw}}
	got, err := p.LoadOrCreate(false)
	testutil.FailErr(t, "load competing identity", err)
	if got != winner {
		t.Fatal("used unstored identity")
	}
}

func TestCredentialProtectionProbeRefusesIsolatedProvider(t *testing.T) {
	if err := VerifyIdentityProtection(); err == nil {
		t.Fatal("an isolated test process qualified the release credential provider")
	}
}
