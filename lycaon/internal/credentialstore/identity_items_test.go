package credentialstore

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type memoryItems struct {
	value                         []byte
	readErr, createErr, removeErr error
	creates, removes              int
}

func (m *memoryItems) read(string) ([]byte, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	if m.value == nil {
		return nil, errIdentityMissing
	}
	return append([]byte(nil), m.value...), nil
}
func (m *memoryItems) create(_ string, value []byte) error {
	m.creates++
	if m.createErr != nil {
		return m.createErr
	}
	m.value = append([]byte(nil), value...)
	return nil
}
func (m *memoryItems) remove(string) error {
	m.removes++
	if m.removeErr != nil {
		return m.removeErr
	}
	m.value = nil
	return nil
}

func TestItemsIdentityCreatesAndReloads(t *testing.T) {
	items := &memoryItems{}
	p := &itemsIdentityProvider{account: "test", description: "test custodian", items: items}
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
	if p.Describe() != "test custodian" {
		t.Fatalf("describe = %q", p.Describe())
	}
}

func TestItemsIdentityFailsClosed(t *testing.T) {
	custodianLocked := errors.New("custodian locked")
	for _, tt := range []struct {
		name    string
		exists  bool
		value   []byte
		readErr error
	}{
		{name: "missing under vault", exists: true},
		{name: "empty item", value: []byte{}},
		{name: "damaged item", value: []byte("{")},
		{name: "custodian error", readErr: custodianLocked},
	} {
		t.Run(tt.name, func(t *testing.T) {
			items := &memoryItems{value: tt.value, readErr: tt.readErr}
			p := &itemsIdentityProvider{account: "test", items: items}
			if _, err := p.LoadOrCreate(tt.exists); err == nil {
				t.Fatal("unusable identity accepted")
			}
			if items.creates != 0 || items.removes != 0 {
				t.Fatal("unusable identity was modified")
			}
		})
	}
}

func TestItemsIdentityDoesNotFallbackOnCreateFailure(t *testing.T) {
	items := &memoryItems{createErr: errors.New("no entitlement")}
	p := &itemsIdentityProvider{account: "test", items: items}
	if _, err := p.LoadOrCreate(false); !errors.Is(err, items.createErr) {
		t.Fatalf("create error = %v", err)
	}
	items = &memoryItems{removeErr: errors.New("busy")}
	p = &itemsIdentityProvider{account: "test", items: items}
	if err := p.Purge(); !errors.Is(err, items.removeErr) {
		t.Fatalf("purge error = %v", err)
	}
}

func TestIdentityAccountsAreIsolated(t *testing.T) {
	a := identityAccount("/a/credential-vault.age")
	same := identityAccount("/a/./credential-vault.age")
	b := identityAccount("/b/credential-vault.age")
	if a != same || a == b {
		t.Fatal("vault identity namespace is not stable and isolated")
	}
}

type competingItems struct {
	memoryItems
	winner []byte
}

func (m *competingItems) create(string, []byte) error {
	m.value = m.winner
	return errIdentityExists
}

func TestItemsConcurrentCreatorUsesStoredWinner(t *testing.T) {
	winner, err := generateIdentityDocument()
	testutil.FailErr(t, "generate winner", err)
	raw, err := encodeIdentityDocument(winner)
	testutil.FailErr(t, "encode winner", err)
	p := &itemsIdentityProvider{account: "test", items: &competingItems{winner: raw}}
	got, err := p.LoadOrCreate(false)
	testutil.FailErr(t, "load competing identity", err)
	if got != winner {
		t.Fatal("used unstored identity")
	}
}

func TestDisposableIdentitiesProveIsolationAndDeletion(t *testing.T) {
	a := &itemsIdentityProvider{account: "a", items: &memoryItems{}}
	b := &itemsIdentityProvider{account: "b", items: &memoryItems{}}
	testutil.FailErr(t, "verify disposable identities", verifyDisposableIdentities(a, b))

	shared := &memoryItems{}
	a = &itemsIdentityProvider{account: "a", items: shared}
	b = &itemsIdentityProvider{account: "b", items: shared}
	if err := verifyDisposableIdentities(a, b); err == nil {
		t.Fatal("a custodian that confuses accounts passed")
	}
}
