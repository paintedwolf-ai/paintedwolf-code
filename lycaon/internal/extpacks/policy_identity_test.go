package extpacks

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDoc9PolicyIdentityIgnoresFilenameAndIncludesNamespace(t *testing.T) {
	root := t.TempDir()
	man := Manifest{ID: "test/rules", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"}}
	writeFixturePack(t, root, "rules", man, map[string]string{
		"policy/first.yaml":  "oar: '1.0'\nid: SAME\nnamespace: first.example\n",
		"policy/second.yaml": "oar: '1.0'\nid: SAME\nnamespace: second.example\n",
	})
	pc, err := InventoryPack(Pack{ID: man.ID, Root: OnDisk(filepath.Join(root, "rules"))}, man)
	if err != nil {
		t.Fatalf("inventory policy fixture: %v", err)
	}
	if len(pc.Units) != 2 || pc.Units[0].ID != "policy/first.example/SAME" || pc.Units[1].ID != "policy/second.example/SAME" {
		t.Fatalf("[OAR-DOC-9] identities: %#v", pc.Units)
	}
}

func TestDoc9PolicyDuplicatesCannotBeHiddenByOwnOrDisable(t *testing.T) {
	for _, status := range []UnitStatus{UnitStatusOwned, UnitStatusDisabled, UnitStatusConflict} {
		t.Run(string(status), func(t *testing.T) {
			e := &EffectiveCatalog{Units: map[string]UnitEffective{"policy/SAME": {
				Status: status, Contributions: []UnitContribution{
					{Path: OnDisk("first.yaml"), Content: []byte("oar: '1.0'\nid: SAME\nmandatory: true\n")},
					{Path: OnDisk("second.yaml"), Content: []byte("oar: '1.0'\nid: SAME\n")},
				},
			}}}
			if err := e.ValidateOARPolicies(); err == nil {
				t.Fatal("[OAR-DOC-9] duplicate accepted")
			}
		})
	}
}

func TestOps7MandatoryPolicyCannotBeDisabled(t *testing.T) {
	e := &EffectiveCatalog{Units: map[string]UnitEffective{"policy/REQUIRED": {
		Status: UnitStatusDisabled, Contributions: []UnitContribution{{Content: []byte("oar: '1.0'\nid: REQUIRED\nmandatory: true\n")}},
	}}}
	if err := e.ValidateOARPolicies(); err == nil || !strings.Contains(err.Error(), "OAR-OPS-7") {
		t.Fatalf("[OAR-OPS-7] got %v", err)
	}
}

func TestDoc1JSONPolicyInventoryAndUnicode(t *testing.T) {
	root := t.TempDir()
	man := Manifest{ID: "test/json", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"}}
	writeFixturePack(t, root, "rules", man, map[string]string{
		"policy/storage.json": `{"oar":"1.0","id":"JSON_RULE","namespace":"test.json","copy":{"what":"\ud83d\udc3a"}}`,
	})
	pc, err := InventoryPack(Pack{ID: man.ID, Root: OnDisk(filepath.Join(root, "rules"))}, man)
	if err != nil {
		t.Fatalf("[OAR-DOC-1] inventory JSON policy: %v", err)
	}
	if len(pc.Units) != 1 || pc.Units[0].ID != "policy/test.json/JSON_RULE" {
		t.Fatalf("[OAR-DOC-1] JSON policy inventory: %#v", pc.Units)
	}
}

func TestDoc1NormalizeJSONPreservesNumbersAndDuplicateKeys(t *testing.T) {
	raw := []byte(`{"copy":{"what":"\ud83d\udc3a \"quoted\""},"priority":9007199254740993,"priority":1}`)
	normalized, err := NormalizePolicyDocument(raw)
	if err != nil {
		t.Fatalf("[OAR-DOC-1] normalize JSON: %v", err)
	}
	got := string(normalized)
	want := `{"copy":{"what":"🐺 \"quoted\""},"priority":9007199254740993,"priority":1}`
	if got != want {
		t.Fatalf("[OAR-DOC-1] normalized JSON = %s, want %s", got, want)
	}
	if _, _, _, err := PolicyDocumentIdentity(raw); err == nil {
		t.Fatal("[OAR-DOC-1] duplicate priority accepted after normalization")
	}
}
