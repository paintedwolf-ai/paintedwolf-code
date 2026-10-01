package mcp_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
)

// stageDistro makes body the bundled distro MCP catalog for one test. The distro
// layer is staged as a config value; user and project overlays stay real files.
func stageDistro(t *testing.T, body string) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{config.DistroMCP: body})
}

// stageFakeDistro stages a distro catalog holding exactly providerIDs.
//
// providerIDs become disabled entries with command "true"; unit tests inject a
// MockConnector, so the command never runs.
func stageFakeDistro(t *testing.T, providerIDs ...string) {
	t.Helper()
	if len(providerIDs) == 0 {
		t.Fatal("stageFakeDistro: need at least one server id")
	}
	var b strings.Builder
	b.WriteString("providers:\n")
	for _, id := range providerIDs {
		b.WriteString("  - id: ")
		b.WriteString(id)
		b.WriteString("\n    command: \"true\"\n    args: []\n    enabled: false\n")
	}
	stageDistro(t, b.String())
}
