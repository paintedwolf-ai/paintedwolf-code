package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// allWorkflowRunStatuses mirrors WorkflowRunStatus in pkg/api/workflow_types.go. Each entry
// gets two facets: terminal (true if it's a sink — never transitions out)
// and initialOK (true if it's a valid initial state for a fresh run, i.e.
// produced at creation time without going through a transition first).
var allWorkflowRunStatuses = []struct {
	status    api.WorkflowRunStatus
	terminal  bool
	initialOK bool
	wireOnly  bool // declared on the wire with no production write
}{
	{api.WorkflowRunStatusRunning, false, true, false},
	{api.WorkflowRunStatusPaused, false, false, false},
	{api.WorkflowRunStatusPausedOnChild, false, false, true},
	{api.WorkflowRunStatusComplete, true, false, false},
	{api.WorkflowRunStatusFailed, true, false, false},
	{api.WorkflowRunStatusCanceled, true, false, false},
	{api.WorkflowRunStatusInterrupted, true, false, false},
}

// TestWorkflowRunStatusGraphReachability scans production code for the status
// state machine: each status has a write and a read, and terminal statuses match
// workflow.IsTerminal.
func TestWorkflowRunStatusGraphReachability(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	productionDirs := []string{
		filepath.Join(root, "lycaon", "internal", "workflow"),
		filepath.Join(root, "lycaon", "internal", "session"),
		filepath.Join(root, "lycaon", "internal", "api"),
	}

	// Writes: anywhere we assign a status. Two patterns:
	//   run.Status = api.WorkflowRunStatusX
	//   Status: api.WorkflowRunStatusX
	writeRE := regexp.MustCompile(`(?:Status:|run\.Status\s*=)\s*api\.WorkflowRunStatus(\w+)`)

	// Reads: anywhere we test against a status (switch case, ==).
	readRE := regexp.MustCompile(`api\.WorkflowRunStatus(\w+)`)

	writes := map[string]bool{}
	reads := map[string]bool{}

	for _, dir := range productionDirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(data)
			for _, m := range writeRE.FindAllStringSubmatch(text, -1) {
				writes[m[1]] = true
			}
			for _, m := range readRE.FindAllStringSubmatch(text, -1) {
				reads[m[1]] = true
			}
			return nil
		})
		contractcheck.FailErr(t, "operation failed", err)
	}

	// Build the canonical "expected" set from the local mirror.
	expected := map[string]struct {
		terminal  bool
		initialOK bool
		wireOnly  bool
	}{}
	for _, e := range allWorkflowRunStatuses {
		name := statusGoSuffix(e.status)
		expected[name] = struct {
			terminal  bool
			initialOK bool
			wireOnly  bool
		}{e.terminal, e.initialOK, e.wireOnly}
	}

	// 1. Every declared status must be observed in writes OR be a documented
	//    initial state seeded by row creation rather than assignment.
	var unreachable []string
	for name, props := range expected {
		if writes[name] {
			continue
		}
		if props.initialOK {
			continue
		}
		if props.wireOnly {
			continue
		}
		unreachable = append(unreachable, name)
	}
	if len(unreachable) > 0 {
		sort.Strings(unreachable)
		t.Errorf("WorkflowRunStatus values with no production write (dead enum?): %v", unreachable)
	}

	// Reads outside the declared enum are unreachable states.
	var ghosts []string
	for name := range reads {
		if _, ok := expected[name]; !ok {
			ghosts = append(ghosts, name)
		}
	}
	if len(ghosts) > 0 {
		sort.Strings(ghosts)
		t.Errorf("WorkflowRunStatus values read but not in pkg/api enum: %v", ghosts)
	}

	// 3. Every status in the enum must be referenced by at least one read.
	//    An enum value no one reads is itself dead.
	var orphan []string
	for name, props := range expected {
		if reads[name] {
			continue
		}
		if props.wireOnly {
			continue
		}
		orphan = append(orphan, name)
	}
	if len(orphan) > 0 {
		sort.Strings(orphan)
		t.Errorf("WorkflowRunStatus values declared but never read: %v", orphan)
	}

	// 4. The mirror's terminal facet matches the production classifier.
	for _, e := range allWorkflowRunStatuses {
		got := workflow.IsTerminal(e.status)
		if got != e.terminal {
			t.Errorf("status %q terminal mismatch: got %v want %v", e.status, got, e.terminal)
		}
	}
}

// statusGoSuffix returns the Go const suffix for a status value.
// Mirrors pkg/api naming: "running" → "Running", etc.
func statusGoSuffix(s api.WorkflowRunStatus) string {
	switch s {
	case api.WorkflowRunStatusRunning:
		return "Running"
	case api.WorkflowRunStatusPaused:
		return "Paused"
	case api.WorkflowRunStatusPausedOnChild:
		return "PausedOnChild"
	case api.WorkflowRunStatusComplete:
		return "Complete"
	case api.WorkflowRunStatusFailed:
		return "Failed"
	case api.WorkflowRunStatusCanceled:
		return "Canceled"
	case api.WorkflowRunStatusInterrupted:
		return "Interrupted"
	}
	return ""
}
