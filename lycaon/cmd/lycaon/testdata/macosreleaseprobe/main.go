//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	internalapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/mcp"
)

type probeErrorClassification struct {
	Failed              bool  `json:"failed"`
	HasKeychainOSStatus bool  `json:"has_keychain_os_status"`
	KeychainOSStatus    int32 `json:"keychain_os_status"`
	IsIdentityMissing   bool  `json:"is_identity_missing"`
	IsCustodianUnavail  bool  `json:"is_custodian_unavailable"`
	IsNotExist          bool  `json:"is_not_exist"`
}

type probeResult struct {
	CustodyMode  string                   `json:"custody_mode"`
	HostIdentity probeErrorClassification `json:"host_identity"`
	MCPLoad      probeErrorClassification `json:"mcp_load"`
	WriteToken   probeErrorClassification `json:"write_token"`
	ReadToken    probeErrorClassification `json:"read_token"`
}

func classifyRefusal(err error) probeErrorClassification {
	if err == nil {
		return probeErrorClassification{}
	}
	status, hasStatus := credentialstore.KeychainOSStatus(err)
	return probeErrorClassification{
		Failed:              true,
		HasKeychainOSStatus: hasStatus,
		KeychainOSStatus:    status,
		IsIdentityMissing:   errors.Is(err, credentialstore.ErrIdentityMissing),
		IsCustodianUnavail:  errors.Is(err, credentialstore.ErrCustodianUnavailable),
		IsNotExist:          errors.Is(err, os.ErrNotExist),
	}
}

func main() {
	readOnly := false
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "--read-only" {
		readOnly = true
		args = args[1:]
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: macosreleaseprobe [--read-only] <config-dir>")
		os.Exit(2)
	}
	dir := args[0]
	ctx := context.Background()

	report, err := credentialstore.DescribeCustody(credentialstore.DefaultVaultPath(dir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "DescribeCustody: %v\n", err)
		os.Exit(1)
	}
	res := probeResult{CustodyMode: string(report.Mode)}

	if !readOnly {
		_, hostErr := hostidentity.LoadOrCreate(dir)
		res.HostIdentity = classifyRefusal(hostErr)

		reg, mcpErr := mcp.NewRuntime(mcp.RuntimeOptions{
			StatePath:          dir,
			GlobalOverridePath: filepath.Join(dir, "mcp.yaml"),
			Connector:          &mcp.MockConnector{},
		})
		if mcpErr == nil {
			mcpErr = reg.Catalog.Load(ctx)
		}
		res.MCPLoad = classifyRefusal(mcpErr)

		_, writeErr := internalapi.WriteAPIToken("release-probe-token")
		res.WriteToken = classifyRefusal(writeErr)
	}
	_, readErr := internalapi.ReadAPIToken()
	res.ReadToken = classifyRefusal(readErr)

	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		os.Exit(1)
	}
}
