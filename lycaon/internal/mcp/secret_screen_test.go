package mcp_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const plantedAWS = "AKIAQYJK5TXV4NZR7SGB"
const plantedJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
const wantAWSRule = "gitleaks:aws-access-token"

func TestMCPCallToolScreenStdio(t *testing.T) {
	stageDistro(t, `providers:
  - id: fixture
    url: http://127.0.0.1:9/mcp
    enabled: false
`)
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	testutil.FailErr(t, "write global", os.WriteFile(globalPath, []byte(`providers:
  - id: fixture
    url: http://127.0.0.1:8765/mcp
    enabled: true
`), 0o600))

	calls := map[string]map[string]int{}
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{
			"fixture": {{Name: "query", Description: "query"}},
		},
		Calls: calls,
	}
	reg, err := mcp.NewRegistryImpl(mcp.RegistryOptions{
		GlobalOverridePath: globalPath,
		Connector:          conn,
	})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.SetToolRegistry(tools.NewDefaultRegistry())

	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher patterns", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("p", 32)))
	testutil.FailErr(t, "build fingerprinter", err)
	matcher.SetFingerprinter(fingerprinter)
	var askCalls atomic.Int32
	var lastRule string
	var sourcePath string
	var destinationID, destinationLabel string
	var fingerprints []secretmatch.SecretFingerprint
	reg.SetSecretScreen(matcher, func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		askCalls.Add(1)
		lastRule = finding.RuleID
		sourcePath = finding.SourcePath
		destinationID, destinationLabel = finding.DestinationID, finding.DestinationLabel
		fingerprints = append([]secretmatch.SecretFingerprint(nil), finding.Fingerprints...)
		return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
	})
	testutil.FailErr(t, "Load", reg.Load(context.Background()))

	_, err = reg.CallTool(context.Background(), mcp.CallScope{}, "fixture", "query", map[string]any{
		"message": plantedAWS,
	})
	rej := toolrejection.AsToolReject(err)
	if rej == nil || rej.Code != "OUTBOUND_SECRET_DENIED" {
		t.Fatalf("want OUTBOUND_SECRET_DENIED, got %v", err)
	}
	if lastRule != wantAWSRule {
		t.Fatalf("rule_id=%q want %s", lastRule, wantAWSRule)
	}
	if stringify(rej.Data["rule_id"]) != wantAWSRule {
		t.Fatalf("reject rule_id=%v", rej.Data["rule_id"])
	}
	if len(rej.Data) != 4 || stringify(rej.Data["shape"]) == "" {
		t.Fatalf("reject data=%#v", rej.Data)
	}
	if calls["fixture"]["query"] != 0 {
		t.Fatalf("CallTool invoked despite deny: %v", calls)
	}
	if askCalls.Load() != 1 {
		t.Fatalf("ask calls=%d", askCalls.Load())
	}
	raw := strings.Join([]string{rej.Code, rej.Error()}, " ")
	for k, v := range rej.Data {
		raw += " " + k + "=" + stringify(v)
	}
	if strings.Contains(raw, plantedAWS) {
		t.Fatalf("value leaked: %s", raw)
	}
	if sourcePath != "$.message" {
		t.Fatalf("source path = %q", sourcePath)
	}
	if destinationLabel != "fixture" || destinationID != secretmatch.DestinationKey(
		"fixture", "http", "http://127.0.0.1:8765/mcp", "", "", "",
		secretmatch.AmbientAuth{}.Identity(matcher),
	) {
		t.Fatalf("destination id=%q label=%q", destinationID, destinationLabel)
	}
	if len(fingerprints) != 1 {
		t.Fatalf("fingerprints = %v, want one exact secret identity", fingerprints)
	}

	conn.CallArgs = map[string]map[string]map[string]any{}
	reg.SetSecretScreen(matcher, func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		if !finding.Surface.CanRedact() || finding.SourceTool != "query" {
			t.Errorf("finding = %+v", finding)
		}
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	original := map[string]any{
		"message": plantedAWS, "Authorization": "Bearer " + plantedJWT, plantedAWS: "public",
	}
	_, err = reg.CallTool(context.Background(), mcp.CallScope{}, "fixture", "query", original)
	testutil.FailErr(t, "redacted CallTool", err)
	sent := conn.CallArgs["fixture"]["query"]
	if value, _ := sent["message"].(string); value != "[REDACTED]" {
		t.Fatalf("sent message = %q", value)
	}
	if value, _ := sent["Authorization"].(string); value != "Bearer [REDACTED]" {
		t.Fatalf("sent Authorization = %q", value)
	}
	if original["message"] != plantedAWS {
		t.Fatal("redaction mutated original MCP args")
	}
	if _, ok := sent[plantedAWS]; ok {
		t.Fatal("sent MCP args retained a credential-shaped key")
	}
	if original[plantedAWS] != "public" {
		t.Fatal("redaction mutated original MCP argument key")
	}
}

func stringify(v any) string {
	s, _ := v.(string)
	return s
}
