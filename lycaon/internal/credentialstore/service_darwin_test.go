package credentialstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestKeychainServiceMatchesBundleIdentifier(t *testing.T) {
	path := filepath.Join("..", "..", "..", "lycaon-den", "src-tauri", "tauri.conf.json")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read tauri.conf.json", err)
	var conf struct {
		Identifier string `json:"identifier"`
	}
	testutil.FailErr(t, "parse tauri.conf.json", json.Unmarshal(raw, &conf))
	if keychainService != conf.Identifier+".credential-vault" {
		t.Fatalf("keychain service = %q", keychainService)
	}
}
