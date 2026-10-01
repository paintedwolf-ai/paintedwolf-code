package external_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/drivers/external"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
)

// leakedCredential is the fixture's reported value.
const leakedCredential = "ghp_A9fK2mQ7zX4bR1nT6yW8pL3vC5dH0jS2gU7e"

func TestSecretScannerFailureNeverCarriesTheCredentialIntoTheError(t *testing.T) {
	root := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	script := filepath.Join(root, "internal", "scan", "testdata", "fake-secret-scanner.sh")

	for _, tc := range []struct {
		name  string
		entry scancatalog.ScannerEntry
	}{
		{
			name: "declared by scope kind",
			entry: scancatalog.ScannerEntry{
				ID:           "byok-fakeleaks-scope",
				Driver:       scancatalog.DriverExternal,
				ScopeKind:    string(scancatalog.ScopeSecrets),
				Categories:   []string{"security"},
				Command:      []string{script},
				OutputParser: scanoutput.OutputParserSARIF,
				OkExitCodes:  []int{0},
			},
		},
		{
			name: "declared by category",
			entry: scancatalog.ScannerEntry{
				ID:           "byok-fakeleaks-category",
				Driver:       scancatalog.DriverExternal,
				Categories:   []string{"secret", "security"},
				Command:      []string{script},
				OutputParser: scanoutput.OutputParserSARIF,
				OkExitCodes:  []int{0},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := external.NewScanner(tc.entry, root, "", "")
			testutil.FailErr(t, "external.NewScanner failed", err)

			res, runErr := sc.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir})
			if runErr == nil {
				t.Fatalf("exit 7 accepted as a clean scan: %d findings", res.FindingsCount)
			}
			message := runErr.Error()
			if strings.Contains(message, leakedCredential) {
				t.Fatalf("scan error disclosed the reported credential: %s", message)
			}
			// Preserve the scanner identity and exit status.
			if !strings.Contains(message, "exit 7") {
				t.Fatalf("scan error dropped the exit code: %s", message)
			}
			if !strings.Contains(message, tc.entry.ID) {
				t.Fatalf("scan error dropped the scanner id: %s", message)
			}
		})
	}
}
