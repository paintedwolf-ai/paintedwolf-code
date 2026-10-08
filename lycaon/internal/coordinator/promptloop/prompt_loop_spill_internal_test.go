package promptloop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/zstdcodec"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTruncateToolResultForSessionClampAppendsRegisteredCode(t *testing.T) {
	results := make([]map[string]any, 0, 80)
	for i := 0; i < 80; i++ {
		results = append(results, map[string]any{
			"path": strings.Repeat("p", 96) + "/file.go",
			"type": "file",
		})
	}
	payload, err := json.Marshal(map[string]any{
		"results": results,
		"offset":  0,
		"receipt": map[string]any{"tool": "find", "paths_touched": 80, "bytes_returned": 99999, "truncated": false},
	})
	testutil.FailErr(t, "json.Marshal failed", err)
	loop := NewPromptLoopForTest(PromptLoopDeps{
		HintConfig: loadCoordinatorTestHintConfig(t),
	})
	got := toolInvocations{loop}.truncateToolResultForSession(t.Context(), "find", toolResultStorageProjection{content: string(payload)}, string(payload), 8192, 0, &api.Session{ID: "s1"})
	if !strings.Contains(got.content, "Code: TOOL_SURVEY_BYTE_CLAMPED") {
		t.Fatalf("expected registered clamp banner, got %q", got.content)
	}
	if !got.facts.HasCode("TOOL_SURVEY_BYTE_CLAMPED") {
		t.Fatalf("clamp must raise its code as a fact, got %v", got.facts.Codes)
	}
	if strings.Contains(got.content, "from offset ;") {
		t.Fatalf("clamp banner must render offset vars, got %q", got.content)
	}
	if strings.Contains(got.content, "...[truncated]") {
		t.Fatalf("clamp path must preserve valid JSON without spill suffix: %q", got.content)
	}
}

func TestTruncateToolResultForSessionOverlaySpillsUnderProject(t *testing.T) {
	dataDir := t.TempDir()
	tail := `{"job_id":"job-1","conflicts":[{"path":"a.go"}],"mode":"preview"}`
	payload := strings.Repeat("x", (512<<10)+len(tail)+64) + tail
	loop := NewPromptLoopForTest(PromptLoopDeps{
		HintConfig: loadCoordinatorTestHintConfig(t),
		DataDir:    dataDir,
	})
	got := toolInvocations{loop}.truncateToolResultForSession(t.Context(), "preview_overlay", toolResultStorageProjection{content: payload}, payload, 4096, 0, &api.Session{
		ID:        "s1",
		ProjectID: "proj-spill",
	})
	if !strings.Contains(got.content, "Code: OVERLAY_PROMOTE_SPILL") {
		t.Fatalf("expected overlay spill banner, got tail=%q", got.content[len(got.content)-120:])
	}
	if !strings.Contains(got.content, "job-1") || !strings.Contains(got.content, tooloutput.PromoteSpillDir) {
		t.Fatalf("expected registry spill hint with job id and spill path, got tail=%q", got.content[len(got.content)-200:])
	}
	spillRel := tooloutput.PromoteSpillRelPath("job-1")
	host := project.HostDataDir(dataDir, "proj-spill")
	if _, err := os.Stat(filepath.Join(host, filepath.FromSlash(spillRel))); err != nil {
		t.Fatalf("expected spill at %s under host data dir: %v", spillRel, err)
	}
}

func TestTruncateToolResultForSessionGenericSpillsInHostData(t *testing.T) {
	dataDir := t.TempDir()
	payload := strings.Repeat("z", 4096)
	loop := NewPromptLoopForTest(PromptLoopDeps{
		HintConfig: loadCoordinatorTestHintConfig(t),
		DataDir:    dataDir,
	})
	got := toolInvocations{loop}.truncateToolResultForSession(t.Context(), "read", toolResultStorageProjection{content: payload}, payload, 512, 0, &api.Session{
		ID:        "s1",
		ProjectID: "proj-spill",
	})
	if !strings.Contains(got.content, "Code: TOOL_OUTPUT_TRUNCATED") {
		t.Fatalf("expected truncation banner, got tail=%q", got.content[max(0, len(got.content)-200):])
	}
	// Spill hints expose only host-data-relative paths.
	if !strings.Contains(got.content, tooloutput.ToolOutputSpillDir+"/") || !strings.Contains(got.content, "read(path=") {
		t.Fatalf("expected spill read hint, got tail=%q", got.content[max(0, len(got.content)-200):])
	}
	if strings.Contains(got.content, ".config/paintedwolf") {
		t.Fatalf("banner must advertise only a relative spill path, got %q", got.content)
	}
	if strings.Contains(got.content, dataDir) || strings.Contains(got.content, "projects/proj-spill") {
		t.Fatalf("banner must advertise relative wire path only, got %q", got.content)
	}
	host := project.HostDataDir(dataDir, "proj-spill")
	matches, _ := filepath.Glob(filepath.Join(host, filepath.FromSlash(tooloutput.ToolOutputSpillDir), "*.txt"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one spill file, found %d", len(matches))
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read spill file: %v", err)
	}
	// Spill bodies are stored zstd-compressed on disk (blobstore.Store).
	data, err := zstdcodec.Decompress(raw)
	if err != nil || string(data) != payload {
		t.Fatalf("spill file must hold full bytes: err=%v len=%d want %d", err, len(data), len(payload))
	}
}

func TestSpillRecoveryUsesRetainedByteBudget(t *testing.T) {
	for _, path := range []string{"", "tool-output/retained.txt"} {
		for _, size := range []int{1, 8 << 20, (8 << 20) + 1} {
			vars := spillHintVars(tooloutput.WireSpillOutcome{SpillPath: path, SpillBytes: size, OriginalBytes: size + 100})
			cap, ok := vars["max_file_bytes"].(int)
			if !ok || cap <= 0 {
				t.Fatalf("missing read input budget: %#v", vars)
			}
			if vars["spill_readable"] != (path != "" && size <= cap) {
				t.Fatalf("unreadable spill advertised: %#v", vars)
			}
			if vars["spill_bytes"] != size || vars["original_bytes"] != size+100 {
				t.Fatal("retained and original sizes conflated")
			}
		}
	}
}
