package promptloop

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	capPropertyResultBytes = 4 << 10
	capPropertySpillBytes  = 64 << 10
)

// truncationCodes are the receipts a fitted result may carry.
var truncationCodes = []string{"TOOL_OUTPUT_TRUNCATED", "TOOL_OUTPUT_SPILL_CAPPED", "TOOL_SURVEY_BYTE_CLAMPED", "OVERLAY_PROMOTE_SPILL"}

// refusalCodes are the typed refusals a result past its limits may earn.
var refusalCodes = []string{tooloutput.ToolResultTooLargeCode, tooloutput.ToolOutputSpillCapExceededCode, tooloutput.ToolOutputSpillUnavailableCode}

// TestEveryCatalogToolResultIsFittedToItsCap drives every catalog tool's
// result, under, at and over the session's result cap and past its spill
// cap, through the gate between a tool and the model. Each result arrives
// whole, or cut to the cap with a receipt and spilled in full, or refused
// with a typed code; none reaches the model past its cap unannounced.
func TestEveryCatalogToolResultIsFittedToItsCap(t *testing.T) {
	ids, err := tools.ParseCatalogToolIDs()
	testutil.FailErr(t, "read the tool catalog", err)
	if len(ids) == 0 {
		t.Fatal("the tool catalog names no tools")
	}
	dataDir := t.TempDir()
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{Limits: func(context.Context, *api.Session) settings.SessionLimits {
			return settings.SessionLimits{MaxToolResultBytes: capPropertyResultBytes, MaxToolSpillBytes: capPropertySpillBytes}
		}},
		Closeout: CloseoutDeps{HintConfig: loadCoordinatorTestHintConfig(t)},
		Tools:    ToolsDeps{DataDir: dataDir},
	})
	sess := &api.Session{ID: "cap-session", ProjectID: "cap-project"}
	hostDir := project.HostDataDir(dataDir, sess.ProjectID)
	sizes := map[string]int{
		"under": capPropertyResultBytes - 1, "at": capPropertyResultBytes,
		"over": capPropertyResultBytes + 1, "past spill": capPropertySpillBytes + 1,
	}
	for _, tool := range ids {
		for name, size := range sizes {
			content := capPropertyOutput(size)
			got := loop.Batch.fitToolOutput(t.Context(), sess, tool, map[string]any{}, content, true)
			if problem := capPropertyViolation(tool, content, got, hostDir); problem != "" {
				t.Errorf("%s, %s its cap (%d bytes): %s", tool, name, size, problem)
			}
		}
	}
}

// capPropertyViolation names how got breaks the cap contract for content.
func capPropertyViolation(tool, content string, got toolResultProjection, hostDir string) string {
	if got.reject != nil {
		if !slices.Contains(refusalCodes, got.reject.Code) {
			return "refused with untyped code " + got.reject.Code
		}
		if len(content) <= capPropertyResultBytes {
			return "refused a result within its cap: " + got.reject.Code
		}
		return ""
	}
	limit := capPropertyResultBytes
	if tooloutput.IsOverlayPromoteTool(tool) {
		limit = tooloutput.OverlayToolResultMaxBytes(capPropertyResultBytes)
	}
	code := ""
	for _, candidate := range truncationCodes {
		if got.facts.HasCode(candidate) {
			code = candidate
		}
	}
	body, _, _ := strings.Cut(got.content, "\n\n"+hostmarker.GuidanceBlockOpen)
	switch {
	case code == "" && got.content != content:
		return "changed a result without a receipt"
	case code == "" && len(content) > limit:
		return "delivered past the cap without a receipt"
	case code != "" && len(body) > limit:
		return code + " delivered " + strconv.Itoa(len(body)) + " bytes past the cap"
	case code == "TOOL_OUTPUT_TRUNCATED" && !spillHolds(hostDir, content):
		return "truncated without spilling the whole result"
	}
	return ""
}

// capPropertyOutput is size bytes of line-addressable text.
func capPropertyOutput(size int) string {
	line := strings.Repeat("x", 63) + "\n"
	out := strings.Repeat(line, size/len(line)+1)
	return out[:size]
}

// spillHolds reports whether a spill file under hostDir holds content.
func spillHolds(hostDir, content string) bool {
	found := false
	_ = filepath.WalkDir(hostDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		found = readErr == nil && string(raw) == content
		return nil
	})
	return found
}
