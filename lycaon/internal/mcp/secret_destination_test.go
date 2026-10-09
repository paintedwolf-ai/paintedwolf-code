package mcp_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpDestinationID returns a screened destination identity.
func mcpDestinationID(t *testing.T, catalog string) string {
	t.Helper()
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	testutil.FailErr(t, "write global", os.WriteFile(globalPath, []byte(catalog), 0o600))

	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{"fixture": {{Name: "query", Description: "query"}}},
		Calls: map[string]map[string]int{},
	}
	reg, err := mcp.NewRegistryImpl(mcp.RegistryOptions{GlobalOverridePath: globalPath, Connector: conn})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.SetToolRegistry(tools.NewDefaultRegistry())

	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher patterns", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("d", 32)))
	testutil.FailErr(t, "build fingerprinter", err)
	matcher.SetFingerprinter(fingerprinter)

	destination := ""
	reg.SetSecretScreen(matcher, func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		destination = finding.DestinationID
		return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
	})
	testutil.FailErr(t, "Load", reg.Load(context.Background()))

	_, err = reg.CallTool(context.Background(), mcp.CallScope{}, "fixture", "query", map[string]any{
		"message": plantedAWS,
	})
	if toolrejection.AsToolReject(err) == nil {
		t.Fatalf("screened call did not block: %v", err)
	}
	if destination == "" {
		t.Fatal("secret screen recorded no destination")
	}
	if strings.Contains(destination, plantedAWS) {
		t.Fatal("destination identity carried a credential")
	}
	return destination
}

func TestMCPDestinationIdentityCoversAmbientAuth(t *testing.T) {
	stageDistro(t, `providers:
  - id: fixture
    url: http://127.0.0.1:9/mcp
    enabled: false
`)
	base := `providers:
  - id: fixture
    url: http://127.0.0.1:8765/mcp
    enabled: true
`
	baseline := mcpDestinationID(t, base)

	variants := map[string]string{
		"credential header": base + "    credential_header: X-Api-Key\n",
		"credential wire":   base + "    credential_wire: header\n    credential_header: X-Api-Key\n",
		"token":             base + "    token: " + plantedAWS + "\n",
		"static header": base + `    headers:
      Authorization: Bearer ` + plantedAWS + "\n",
	}
	seen := map[string]string{baseline: "baseline"}
	for name, catalog := range variants {
		t.Run(name, func(t *testing.T) {
			got := mcpDestinationID(t, catalog)
			if got == baseline {
				t.Fatalf("%s change kept the release identity %q", name, baseline)
			}
			if other, dup := seen[got]; dup {
				t.Fatalf("%s shares a release identity with %s", name, other)
			}
			seen[got] = name
		})
	}
}

func TestMCPStdioDestinationIdentityCoversEnvironment(t *testing.T) {
	stageDistro(t, `providers:
  - id: fixture
    command: "true"
    args: []
    enabled: false
`)
	base := `providers:
  - id: fixture
    command: "true"
    args: []
    enabled: true
`
	withEnv := base + `    env:
      SERVICE_TOKEN: ` + plantedAWS + "\n"
	if bare, env := mcpDestinationID(t, base), mcpDestinationID(t, withEnv); bare == env {
		t.Fatalf("a credential in the server environment kept the release identity %q", bare)
	}
}

func TestMCPDestinationIdentitySeparatesCredentialValues(t *testing.T) {
	stageDistro(t, `providers:
  - id: fixture
    url: http://127.0.0.1:9/mcp
    enabled: false
`)
	catalog := func(token string) string {
		return `providers:
  - id: fixture
    url: http://127.0.0.1:8765/mcp
    enabled: true
    token: ` + token + "\n"
	}
	first := mcpDestinationID(t, catalog("AKIAQYJK5TXV4NZR7SGB"))
	second := mcpDestinationID(t, catalog("AKIA5TXV4NZR7SGBQYJK"))
	if first == second {
		t.Fatalf("rotating the credential kept the release identity %q", first)
	}
}
