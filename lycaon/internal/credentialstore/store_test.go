package credentialstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStoreRoundTripIsPrivateAndValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credential-vault.age")
	slot := Slot{Path: path, Namespace: "test", Context: "test"}
	store := NewEmpty(slot, func(id string) bool { return id == "known" })
	if err := store.Set("unknown", "secret"); err == nil {
		t.Fatal("Set accepted an unknown credential slot")
	}
	if err := store.Set("known", "secret"); err != nil {
		t.Fatalf("Set known: %v", err)
	}
	assertOwnerOnly(t, path)
	reloaded, err := OpenFile(slot, func(id string) bool { return id == "known" })
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got, ok := reloaded.Get("known"); !ok || got != "secret" {
		t.Fatalf("reloaded credential = %q, %v", got, ok)
	}
	if err := reloaded.Delete("known"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := reloaded.Get("known"); ok {
		t.Fatal("deleted credential remains in memory")
	}
}

func TestStorePreservesShortAndWhitespaceBearingValuesExactly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-vault.age")
	slot := Slot{Path: path, Namespace: "test", Context: "test"}
	store := NewEmpty(slot, nil)
	for id, want := range map[string]string{
		"short": "x",
		"space": " \tcredential with edges\n ",
	} {
		if err := store.Set(id, want); err != nil {
			t.Fatalf("Set %s: %v", id, err)
		}
	}
	reloaded, err := OpenFile(slot, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for id, want := range map[string]string{
		"short": "x",
		"space": " \tcredential with edges\n ",
	} {
		if got, ok := reloaded.Get(id); !ok || got.Value() != want {
			t.Fatalf("Get(%q) = %q, %v; want exact %q", id, got.Value(), ok, want)
		}
	}
}

func TestStoreRollsBackMemoryWhenPersistenceFails(t *testing.T) {
	store := NewEmpty(Slot{Path: t.TempDir(), Namespace: "test", Context: "test"}, nil)
	if err := store.Set("known", "secret"); err == nil {
		t.Fatal("Set unexpectedly replaced a directory")
	}
	if _, ok := store.Get("known"); ok {
		t.Fatal("failed Set remained in memory")
	}
}

func TestOpenIgnoresCredentialsOutsideTheAcceptedCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-vault.age")
	slot := Slot{Path: path, Namespace: "test", Context: "test"}
	seed := NewEmpty(slot, nil)
	if err := seed.Set("known", "yes"); err != nil {
		t.Fatalf("seed known: %v", err)
	}
	if err := seed.Set("retired", "no"); err != nil {
		t.Fatalf("seed retired: %v", err)
	}
	store, err := OpenFile(slot, func(id string) bool { return id == "known" })
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := store.Get("known"); !ok {
		t.Fatal("accepted credential was not loaded")
	}
	if _, ok := store.Get("retired"); ok {
		t.Fatal("credential outside the accepted catalog was loaded")
	}
}

func TestSecretValueRedactsInEncoders(t *testing.T) {
	const raw = "super-secret-key-12345"
	val := SecretValue(raw)
	if val.Value() != raw {
		t.Fatalf("Value() = %q, want unredacted", val.Value())
	}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("credential", "key", val)
	jsonBytes, err := json.Marshal(map[string]any{"key": val})
	testutil.FailErr(t, "marshal json", err)
	for encoder, out := range map[string]string{
		"fmt %v": fmt.Sprint(val),
		"fmt %q": fmt.Sprintf("%q", val),
		"json":   string(jsonBytes),
		"slog":   logged.String(),
	} {
		if strings.Contains(out, raw) || !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s output = %q, want redacted", encoder, out)
		}
	}
}
