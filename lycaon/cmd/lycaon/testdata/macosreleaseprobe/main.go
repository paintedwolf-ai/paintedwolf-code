//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	internalapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/mcp"
)

type probeResult struct {
	CustodyMode     string `json:"custody_mode"`
	HostIdentityErr string `json:"host_identity_err"`
	MCPLoadErr      string `json:"mcp_load_err"`
	WriteTokenErr   string `json:"write_token_err"`
	ReadTokenErr    string `json:"read_token_err"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: macosreleaseprobe <config-dir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	ctx := context.Background()

	report, err := credentialstore.DescribeCustody(credentialstore.DefaultVaultPath(dir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "DescribeCustody: %v\n", err)
		os.Exit(1)
	}
	res := probeResult{CustodyMode: string(report.Mode)}

	if _, err := hostidentity.LoadOrCreate(dir); err != nil {
		res.HostIdentityErr = err.Error()
	}
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          dir,
		GlobalOverridePath: filepath.Join(dir, "mcp.yaml"),
		Connector:          &mcp.MockConnector{},
	})
	if err != nil {
		res.MCPLoadErr = err.Error()
	} else if loadErr := reg.Catalog.Load(ctx); loadErr != nil {
		res.MCPLoadErr = loadErr.Error()
	}
	if _, err := internalapi.WriteAPIToken("release-probe-token"); err != nil {
		res.WriteTokenErr = err.Error()
	}
	if _, err := internalapi.ReadAPIToken(); err != nil {
		res.ReadTokenErr = err.Error()
	}

	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		os.Exit(1)
	}
}
