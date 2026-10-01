package scanworker

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/drivers/library"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWorkerUsesPrivateKeyWithoutCredentialVault(t *testing.T) {
	root := t.TempDir()
	const value = "AKIAQYJK5TXV4NZR7SGB"
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, "fixture.env"), []byte("AWS_ACCESS_KEY_ID="+value), 0o600))
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{7}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	request, err := json.Marshal(Request{Impl: library.ImplGitleaks, ID: "secrets", Jobs: 1, Scan: scan.ScanRequest{ProjectDir: root}, FingerprintKey: fp.ScannerKey()})
	testutil.FailErr(t, "request", err)
	var output bytes.Buffer
	testutil.FailErr(t, "worker", Serve(t.Context(), bytes.NewReader(append(request, '\n')), &output))
	var response Response
	testutil.FailErr(t, "response", json.Unmarshal(output.Bytes(), &response))
	if response.Error != "" || response.Result == nil {
		t.Fatalf("worker response: %s", response.Error)
	}
	if bytes.Contains(output.Bytes(), []byte(value)) {
		t.Fatal("worker returned plaintext credential")
	}
	identities := response.SecretIdentities
	if len(identities) != 1 || identities[0].FindingIndex != 0 || identities[0].ValueFingerprint != string(fp.ScannerFingerprint(value)) {
		t.Fatalf("unexpected identity count %d", len(identities))
	}
	if fp.ScannerFingerprint(value) == fp.Fingerprint(value) {
		t.Fatal("scanner reused runtime fingerprint domain")
	}
}

func TestPublicResultOmitsPrivateValueIdentities(t *testing.T) {
	result := &scanoutput.Result{SecretIdentities: []scanoutput.SecretIdentity{{FindingIndex: 0, ValueFingerprint: "private-value-fingerprint"}}}
	raw, err := json.Marshal(result)
	testutil.FailErr(t, "serialize public result", err)
	if bytes.Contains(raw, []byte("private-value-fingerprint")) || bytes.Contains(raw, []byte("secret_identities")) {
		t.Fatal("public result disclosed private identity")
	}
}
