//go:build darwin

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	internalapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type macOSReleaseProbeErrorClassification struct {
	Failed              bool  `json:"failed"`
	HasKeychainOSStatus bool  `json:"has_keychain_os_status"`
	KeychainOSStatus    int32 `json:"keychain_os_status"`
	IsIdentityMissing   bool  `json:"is_identity_missing"`
	IsCustodianUnavail  bool  `json:"is_custodian_unavailable"`
	IsNotExist          bool  `json:"is_not_exist"`
}

type macOSReleaseProbeResult struct {
	CustodyMode  string                               `json:"custody_mode"`
	HostIdentity macOSReleaseProbeErrorClassification `json:"host_identity"`
	MCPLoad      macOSReleaseProbeErrorClassification `json:"mcp_load"`
	WriteToken   macOSReleaseProbeErrorClassification `json:"write_token"`
	ReadToken    macOSReleaseProbeErrorClassification `json:"read_token"`
}

func TestMacOSUnsignedReleaseCustodyRefusalPreservesLegacyFilesAndRefusesTokenPrint(t *testing.T) {
	binDir := t.TempDir()
	probeBin := filepath.Join(binDir, "macosreleaseprobe")
	pwBin := filepath.Join(binDir, "pw-release-unsigned")

	buildProbe := exec.Command("go", "build", "-tags=paintedwolf_release", "-o", probeBin, "./testdata/macosreleaseprobe")
	if out, err := buildProbe.CombinedOutput(); err != nil {
		t.Fatalf("build macosreleaseprobe: %v\n%s", err, string(out))
	}
	buildPW := exec.Command("go", "build", "-tags=paintedwolf_release", "-o", pwBin, ".")
	if out, err := buildPW.CombinedOutput(); err != nil {
		t.Fatalf("build release pw: %v\n%s", err, string(out))
	}

	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, ".config", "paintedwolf")
	testutil.FailErr(t, "mkdir configDir", os.MkdirAll(configDir, 0o700))
	testutil.FailErr(t, "seed legacy files", seedMacOSLegacySecretFiles(configDir))

	legacyFiles := []string{hostidentity.FileName, "mcp.yaml", internalapi.LegacyTokenFileName}
	beforeDigests := make(map[string]string, len(legacyFiles))
	beforeBytes := make(map[string][]byte, len(legacyFiles))
	for _, name := range legacyFiles {
		raw, err := os.ReadFile(filepath.Join(configDir, name))
		testutil.FailErr(t, "read before "+name, err)
		sum := sha256.Sum256(raw)
		beforeDigests[name] = hex.EncodeToString(sum[:])
		beforeBytes[name] = raw
	}

	cleanEnv := envWithHomeOnly(homeDir)
	probeCmd := exec.Command(probeBin, configDir)
	probeCmd.Env = cleanEnv
	probeOut, err := probeCmd.Output()
	testutil.FailErr(t, "run macosreleaseprobe", err)

	var res macOSReleaseProbeResult
	testutil.FailErr(t, "decode probe result", json.Unmarshal(probeOut, &res))
	if res.CustodyMode != string(credentialstore.CustodyKeychain) {
		t.Fatalf("release custody_mode = %q, want %q", res.CustodyMode, credentialstore.CustodyKeychain)
	}
	for label, got := range map[string]macOSReleaseProbeErrorClassification{
		"hostidentity.LoadOrCreate": res.HostIdentity,
		"mcp.Catalog.Load":          res.MCPLoad,
		"api.WriteAPIToken":         res.WriteToken,
	} {
		if !got.Failed || !got.HasKeychainOSStatus || got.KeychainOSStatus != credentialstore.KeychainMissingEntitlementStatus ||
			got.IsIdentityMissing || got.IsNotExist {
			t.Fatalf("%s classification = %+v, want KeychainOSStatus %d", label, got, credentialstore.KeychainMissingEntitlementStatus)
		}
	}
	if !res.ReadToken.Failed || !res.ReadToken.IsNotExist || res.ReadToken.HasKeychainOSStatus || res.ReadToken.IsIdentityMissing {
		t.Fatalf("api.ReadAPIToken (vault absent) classification = %+v, want os.ErrNotExist refusal", res.ReadToken)
	}

	// Run unsigned release `pw token print` against the same synthetic HOME.
	var tokenStdout, tokenStderr bytes.Buffer
	tokenCmd := exec.Command(pwBin, "token", "print")
	tokenCmd.Env = cleanEnv
	tokenCmd.Stdout = &tokenStdout
	tokenCmd.Stderr = &tokenStderr
	if err := tokenCmd.Run(); err == nil {
		t.Fatalf("unsigned release `pw token print` unexpectedly succeeded; stdout=%q", tokenStdout.String())
	}
	if tokenStdout.Len() != 0 {
		t.Fatalf("unsigned release `pw token print` wrote stdout: %q", tokenStdout.String())
	}

	// Verify no vault or development identity was created and legacy files are byte-identical.
	for _, forbidden := range []string{"credential-vault.age", ".credential-vault-development-identity"} {
		if _, err := os.Stat(filepath.Join(configDir, forbidden)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("forbidden file %s exists after release refusal: %v", forbidden, err)
		}
	}
	entries, err := os.ReadDir(configDir)
	testutil.FailErr(t, "readdir configDir", err)
	for _, entry := range entries {
		name := entry.Name()
		if name == "credential-vault.age.lock" {
			info, statErr := entry.Info()
			testutil.FailErr(t, "stat lock file", statErr)
			if info.Size() != 0 {
				t.Fatalf("lock file size = %d, want 0", info.Size())
			}
			continue
		}
		if _, ok := beforeDigests[name]; !ok {
			t.Fatalf("unexpected file %q created in configDir after release refusal", name)
		}
	}
	for _, name := range legacyFiles {
		raw, err := os.ReadFile(filepath.Join(configDir, name))
		testutil.FailErr(t, "read after "+name, err)
		sum := sha256.Sum256(raw)
		afterDigest := hex.EncodeToString(sum[:])
		if afterDigest != beforeDigests[name] || !bytes.Equal(raw, beforeBytes[name]) {
			t.Fatalf("legacy file %s changed after release refusal: before=%s after=%s", name, beforeDigests[name], afterDigest)
		}
	}

	// Also verify ReadAPIToken and pw token print refuse with ErrIdentityMissing
	// when credential-vault.age is present alongside api.token.
	homeWithVault := t.TempDir()
	cfgWithVault := filepath.Join(homeWithVault, ".config", "paintedwolf")
	testutil.FailErr(t, "mkdir cfgWithVault", os.MkdirAll(cfgWithVault, 0o700))
	testutil.FailErr(t, "seed legacy api.token", os.WriteFile(filepath.Join(cfgWithVault, internalapi.LegacyTokenFileName), []byte("legacy-token\n"), 0o600))
	testutil.FailErr(t, "seed placeholder vault", os.WriteFile(credentialstore.DefaultVaultPath(cfgWithVault), []byte("age-encryption.org/v1\n"), 0o600))

	vaultEnv := envWithHomeOnly(homeWithVault)
	vaultProbeCmd := exec.Command(probeBin, "--read-only", cfgWithVault)
	vaultProbeCmd.Env = vaultEnv
	vaultProbeOut, err := vaultProbeCmd.Output()
	testutil.FailErr(t, "run macosreleaseprobe --read-only", err)

	var vaultRes macOSReleaseProbeResult
	testutil.FailErr(t, "decode vault-present probe result", json.Unmarshal(vaultProbeOut, &vaultRes))
	if !vaultRes.ReadToken.Failed || !vaultRes.ReadToken.IsIdentityMissing || vaultRes.ReadToken.IsNotExist {
		t.Fatalf("api.ReadAPIToken (vault present) classification = %+v, want credentialstore.ErrIdentityMissing", vaultRes.ReadToken)
	}

	var vaultTokenStdout, vaultTokenStderr bytes.Buffer
	vaultTokenCmd := exec.Command(pwBin, "token", "print")
	vaultTokenCmd.Env = vaultEnv
	vaultTokenCmd.Stdout = &vaultTokenStdout
	vaultTokenCmd.Stderr = &vaultTokenStderr
	if err := vaultTokenCmd.Run(); err == nil {
		t.Fatal("unsigned release `pw token print` succeeded with vault file present")
	}
	if vaultTokenStdout.Len() != 0 {
		t.Fatalf("unsigned release `pw token print` with vault wrote stdout: %q", vaultTokenStdout.String())
	}

	t.Logf("macOS release-custody refusal verified: custody=%s host_identity=%+v mcp_load=%+v write_token=%+v read_token=%+v read_token_with_vault=%+v digests=%v",
		res.CustodyMode, res.HostIdentity, res.MCPLoad, res.WriteToken, res.ReadToken, vaultRes.ReadToken, beforeDigests)
}

func TestMacOSDevelopmentCustodianWorkflowParity(t *testing.T) {
	if phase := os.Getenv("PW_MACOS_DEV_WORKFLOW_PHASE"); phase != "" {
		dir := os.Getenv("PW_MACOS_DEV_WORKFLOW_DIR")
		out, err := executeMacOSDevelopmentWorkflowPhase(phase, dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(out)
		os.Exit(0)
	}

	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, ".config", "paintedwolf-dev")
	testutil.FailErr(t, "mkdir configDir", os.MkdirAll(configDir, 0o700))
	testutil.FailErr(t, "seed legacy files", seedMacOSLegacySecretFiles(configDir))

	runPhase := func(phase string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestMacOSDevelopmentCustodianWorkflowParity$")
		cmd.Env = append(envWithHomeOnly(homeDir),
			"PW_MACOS_DEV_WORKFLOW_PHASE="+phase,
			"PW_MACOS_DEV_WORKFLOW_DIR="+configDir,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("development workflow phase %s failed: %v\n%s", phase, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	firstOut := runPhase("migrate_and_persist")
	secondOut := runPhase("verify_reopen_and_token_print")

	firstParts := strings.Split(firstOut, "|")
	secondParts := strings.Split(secondOut, "|")
	if len(firstParts) != 2 || len(secondParts) != 3 {
		t.Fatalf("unexpected phase outputs: first=%q second=%q", firstOut, secondOut)
	}
	if firstParts[0] != secondParts[0] || firstParts[1] != secondParts[1] {
		t.Fatalf("host identity changed across fresh process reopen: first=%v second=%v", firstParts, secondParts[:2])
	}
	if secondParts[2] != "macos-dev-daemon-token-987" {
		t.Fatalf("fresh process `pw token print` = %q, want %q", secondParts[2], "macos-dev-daemon-token-987")
	}
	t.Logf("macOS development-custody workflow parity verified (explicitly labelled development custody: .credential-vault-development-identity): host_id=%s token_print=%s",
		secondParts[0], secondParts[2])
}

func executeMacOSDevelopmentWorkflowPhase(phase, dir string) (string, error) {
	if err := os.Setenv(configdir.EnvConfigDir, dir); err != nil {
		return "", err
	}
	if err := os.Setenv("LYCAON_API_TOKEN", ""); err != nil {
		return "", err
	}
	ctx := context.Background()
	globalMCPPath := filepath.Join(dir, "mcp.yaml")

	switch phase {
	case "migrate_and_persist":
		id, err := hostidentity.LoadOrCreate(dir)
		if err != nil {
			return "", fmt.Errorf("hostidentity.LoadOrCreate: %w", err)
		}
		if _, err := os.Stat(filepath.Join(dir, hostidentity.FileName)); !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("legacy %s still present after import: %v", hostidentity.FileName, err)
		}
		reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
			StatePath:          dir,
			GlobalOverridePath: globalMCPPath,
			Connector:          &mcp.MockConnector{},
		})
		if err != nil {
			return "", fmt.Errorf("mcp.NewRuntime: %w", err)
		}
		if err := reg.Catalog.Load(ctx); err != nil {
			return "", fmt.Errorf("mcp.Catalog.Load: %w", err)
		}
		scrubbedYAML, err := os.ReadFile(globalMCPPath)
		if err != nil {
			return "", fmt.Errorf("read scrubbed mcp.yaml: %w", err)
		}
		for _, canary := range []string{"legacy-mcp-bearer-token", "legacy-header-secret", "legacy-env-secret"} {
			if strings.Contains(string(scrubbedYAML), canary) {
				return "", fmt.Errorf("mcp.yaml retained inline secret %q", canary)
			}
		}
		rotatedTok := "rotated-mcp-token-v2"
		if _, err := reg.Administration.UpdateProvider(ctx, mcp.CallScope{}, "wf-http", api.UpdateMcpProviderRequest{
			Token: &rotatedTok,
		}, ""); err != nil {
			return "", fmt.Errorf("mcp.UpdateProvider: %w", err)
		}
		if _, err := internalapi.WriteAPIToken("macos-dev-daemon-token-987"); err != nil {
			return "", fmt.Errorf("WriteAPIToken: %w", err)
		}
		if _, err := os.Stat(filepath.Join(dir, internalapi.LegacyTokenFileName)); !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("legacy %s still present after WriteAPIToken: %v", internalapi.LegacyTokenFileName, err)
		}
		tok, err := internalapi.ReadAPIToken()
		if err != nil || tok != "macos-dev-daemon-token-987" {
			return "", fmt.Errorf("ReadAPIToken after write = (%q, %v)", tok, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".credential-vault-development-identity")); err != nil {
			return "", fmt.Errorf("expected .credential-vault-development-identity under development custody: %w", err)
		}
		return id.HostID + "|" + id.EncodedPublicKey(), nil

	case "verify_reopen_and_token_print":
		id, err := hostidentity.LoadOrCreate(dir)
		if err != nil {
			return "", fmt.Errorf("reopen hostidentity.LoadOrCreate: %w", err)
		}
		reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
			StatePath:          dir,
			GlobalOverridePath: globalMCPPath,
			Connector:          &mcp.MockConnector{},
		})
		if err != nil {
			return "", fmt.Errorf("reopen mcp.NewRuntime: %w", err)
		}
		if err := reg.Catalog.Load(ctx); err != nil {
			return "", fmt.Errorf("reopen mcp.Catalog.Load: %w", err)
		}
		store, err := mcp.NewStaticCredentialStoreForDir(dir)
		if err != nil {
			return "", fmt.Errorf("reopen NewStaticCredentialStoreForDir: %w", err)
		}
		rec, ok, err := store.Get("wf-http")
		if err != nil || !ok || rec.Token != "rotated-mcp-token-v2" ||
			rec.Headers["X-Custom-Auth"] != "legacy-header-secret" || rec.PendingCommit != nil {
			return "", fmt.Errorf("unexpected reopened wf-http record (ok=%v err=%v): %+v", ok, err, rec)
		}
		stdioRec, ok, err := store.Get("wf-stdio")
		if err != nil || !ok || stdioRec.Env["MCP_API_KEY"] != "legacy-env-secret" || stdioRec.PendingCommit != nil {
			return "", fmt.Errorf("unexpected reopened wf-stdio record (ok=%v err=%v): %+v", ok, err, stdioRec)
		}
		if _, err := internalapi.ReadAPIToken(); err != nil {
			return "", fmt.Errorf("reopen ReadAPIToken: %w", err)
		}
		var printed bytes.Buffer
		if err := runToken([]string{"print"}, &printed); err != nil {
			return "", fmt.Errorf("reopen runToken(print): %w", err)
		}
		return id.HostID + "|" + id.EncodedPublicKey() + "|" + strings.TrimSpace(printed.String()), nil

	default:
		return "", fmt.Errorf("unknown phase %q", phase)
	}
}

func seedMacOSLegacySecretFiles(dir string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, hostidentity.FileName), pemBytes, 0o600); err != nil {
		return err
	}
	const inlineMCPYAML = `providers:
  - id: wf-http
    url: https://mcp.example.com/v1
    enabled: false
    token: legacy-mcp-bearer-token
    headers:
      X-Custom-Auth: legacy-header-secret
  - id: wf-stdio
    command: npx
    enabled: false
    env:
      MCP_API_KEY: legacy-env-secret
`
	if err := os.WriteFile(filepath.Join(dir, "mcp.yaml"), []byte(inlineMCPYAML), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, internalapi.LegacyTokenFileName), []byte("legacy-plaintext-api-token\n"), 0o600)
}

func envWithHomeOnly(homeDir string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") ||
			strings.HasPrefix(kv, "XDG_CONFIG_HOME=") ||
			strings.HasPrefix(kv, "LYCAON_CONFIG_DIR=") ||
			strings.HasPrefix(kv, "LYCAON_API_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+homeDir)
}
