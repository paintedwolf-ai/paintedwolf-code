package synthesiswrapupdealfinder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

const fixtureDirName = "synthesis-wrapup-deal-finder"

// Fixture holds wrapup surface and tool-availability cases.
type Fixture struct {
	SessionID  string
	Surface    []SurfaceCase
	GuardCases []GuardCase
}

type SurfaceCase struct {
	Name             string
	History          []api.Message
	Progress         string
	State            SnapshotState
	UserPrompt       string
	WantSurface      string
	WantBatchReady   bool
	WantOpenRepair   bool
	WantVerifyPassed bool
	WantVerifyFailed bool
}

type GuardCase struct {
	Name        string
	Surface     string
	Tool        string
	ToolOffered bool
	WantCode    string
}

type SnapshotState struct {
	WorkersInFlight   int      `json:"workers_in_flight"`
	PendingOverlayIDs []string `json:"pending_overlay_ids"`
	BatchPhase        string   `json:"batch_phase"`
}

type expectedSurfacesDoc struct {
	SessionID string `yaml:"session_id"`
	Cases     []struct {
		Name             string `yaml:"name"`
		HistoryFile      string `yaml:"history_file"`
		ProgressFile     string `yaml:"progress_file"`
		StateFile        string `yaml:"state_file"`
		UserPrompt       string `yaml:"user_prompt"`
		WantSurface      string `yaml:"want_surface"`
		WantBatchReady   bool   `yaml:"want_batch_ready"`
		WantOpenRepair   bool   `yaml:"want_open_repair"`
		WantVerifyPassed bool   `yaml:"want_verify_passed"`
		WantVerifyFailed bool   `yaml:"want_verify_failed"`
	} `yaml:"cases"`
	GuardCases []struct {
		Name        string `yaml:"name"`
		Surface     string `yaml:"surface"`
		Tool        string `yaml:"tool"`
		ToolOffered bool   `yaml:"tool_offered"`
		WantCode    string `yaml:"want_code"`
	} `yaml:"guard_cases"`
}

// Load reads wrapup cases and their transcript snapshots.
func Load(t *testing.T) Fixture {
	t.Helper()
	root := fixtureRoot(t)
	docPath := filepath.Join(root, "expected_surfaces.yaml")
	raw, err := os.ReadFile(docPath) // #nosec G304 -- fixed fixture path
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	var doc expectedSurfacesDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse expected_surfaces.yaml: %v", err)
	}
	out := Fixture{SessionID: doc.SessionID}
	for _, row := range doc.Cases {
		out.Surface = append(out.Surface, SurfaceCase{
			Name:             row.Name,
			History:          loadHistory(t, root, row.HistoryFile),
			Progress:         loadText(t, root, row.ProgressFile),
			State:            loadState(t, root, row.StateFile),
			UserPrompt:       row.UserPrompt,
			WantSurface:      row.WantSurface,
			WantBatchReady:   row.WantBatchReady,
			WantOpenRepair:   row.WantOpenRepair,
			WantVerifyPassed: row.WantVerifyPassed,
			WantVerifyFailed: row.WantVerifyFailed,
		})
	}
	for _, row := range doc.GuardCases {
		gc := GuardCase{
			Name:        row.Name,
			Surface:     row.Surface,
			Tool:        row.Tool,
			ToolOffered: row.ToolOffered,
			WantCode:    row.WantCode,
		}
		out.GuardCases = append(out.GuardCases, gc)
	}
	return out
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "testdata", "coordinator", fixtureDirName)
}

func loadText(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name)) // #nosec G304 -- fixture name
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func loadState(t *testing.T, root, name string) SnapshotState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name)) // #nosec G304 -- fixture name
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var state SnapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return state
}

func loadHistory(t *testing.T, root, name string) []api.Message {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var history []api.Message
	if err := json.Unmarshal(data, &history); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return history
}
