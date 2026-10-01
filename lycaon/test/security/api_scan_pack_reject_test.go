package security

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanPackSchemaRejectsUnknownCategory(t *testing.T) {
	h := wiring.BuildForTest(t)
	projectDir := t.TempDir()

	_, err := h.ToolRegistry.Run(context.Background(), "scan_pack", map[string]any{
		"categories": []any{"not-a-real-category"},
	}, securityToolContext("sess-1", projectDir, "coordinator"))
	if err == nil {
		t.Fatal("expected reject")
	}
	if !strings.Contains(err.Error(), "SCAN_PACK_CATEGORY_INVALID") {
		t.Fatalf("err = %q, want SCAN_PACK_CATEGORY_INVALID", err.Error())
	}
}
