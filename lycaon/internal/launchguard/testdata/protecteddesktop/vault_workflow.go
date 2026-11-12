//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	internalapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/vaultidentity"
	"github.com/lycaon/lycaon/pkg/api"
)

func runSecretWorkflow(cmd vaultCommand) {
	val, err := executeSecretWorkflow(cmd.WorkflowOp, cmd.WorkflowDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(vaultidentity.Response{
			Status:  vaultidentity.StatusUnavailable,
			Message: err.Error(),
		})
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(vaultidentity.Response{
		Status: vaultidentity.StatusOK,
		Value:  []byte(val),
	})
}

func executeSecretWorkflow(op, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("workflow_dir is required")
	}
	if err := os.Setenv("LYCAON_CONFIG_DIR", dir); err != nil {
		return "", err
	}
	ctx := context.Background()
	globalMCPPath := filepath.Join(dir, "mcp.yaml")

	switch op {
	case "migrate_and_persist":
		if err := credentialstore.VerifyIdentityProtection(); err != nil {
			return "", fmt.Errorf("VerifyIdentityProtection: %w", err)
		}
		id, err := hostidentity.LoadOrCreate(dir)
		if err != nil {
			return "", fmt.Errorf("hostidentity.LoadOrCreate: %w", err)
		}
		if _, err := os.Stat(filepath.Join(dir, hostidentity.FileName)); !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("legacy %s still present after import: %v", hostidentity.FileName, err)
		}
		if _, err := os.Stat(credentialstore.DefaultVaultPath(dir)); err != nil {
			return "", fmt.Errorf("stat credential-vault.age: %w", err)
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

		if _, err := internalapi.WriteAPIToken("daemon-bearer-token-from-protected-launch"); err != nil {
			return "", fmt.Errorf("WriteAPIToken: %w", err)
		}
		if _, err := os.Stat(filepath.Join(dir, internalapi.LegacyTokenFileName)); !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("legacy %s still present after WriteAPIToken: %v", internalapi.LegacyTokenFileName, err)
		}
		tok, err := internalapi.ReadAPIToken()
		if err != nil || tok != "daemon-bearer-token-from-protected-launch" {
			return "", fmt.Errorf("ReadAPIToken after write = (%q, %v)", tok, err)
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
		if err != nil || !ok {
			return "", fmt.Errorf("reopen store.Get(wf-http) = (ok=%v, err=%v)", ok, err)
		}
		if rec.Token != "rotated-mcp-token-v2" ||
			rec.Headers["X-Custom-Auth"] != "legacy-header-secret" ||
			rec.Env["MCP_API_KEY"] != "legacy-env-secret" ||
			rec.PendingCommit != nil {
			return "", fmt.Errorf("unexpected reopened mcp record: %+v", rec)
		}
		tok, err := internalapi.ReadAPIToken()
		if err != nil {
			return "", fmt.Errorf("reopen ReadAPIToken: %w", err)
		}
		return id.HostID + "|" + id.EncodedPublicKey() + "|" + tok, nil

	default:
		return "", fmt.Errorf("unknown workflow_op %q", op)
	}
}
