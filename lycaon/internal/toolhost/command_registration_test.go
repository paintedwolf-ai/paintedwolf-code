package toolhost

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestRegisteredProcessListReturnsStructuredInventory(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var cmd *command.CommandTool
	var verify *native.VerifyTool
	var output *command.CommandOutputTool
	var stop *command.CommandStopTool
	if err := registerCommandTools(reg, nil, nil, &cmd, &verify, &output, &stop); err != nil {
		t.Fatal(err)
	}
	reviewed := false
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}, Execution: tools.InvocationExecution{ProcessReview: func(_ context.Context, operation string, targets []hostprocess.Process) error {
		if operation != "list" || len(targets) != 0 {
			t.Fatal("incorrect process review subject")
		}
		reviewed = true
		return nil
	}}}
	raw, err := reg.Run(t.Context(), "process_list", map[string]any{"pid": os.Getpid()}, tc)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(raw)) {
		t.Fatalf("process inventory not JSON: %q", raw)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	if !reviewed {
		t.Fatal("process list bypassed review")
	}
	if len(fields) == 0 {
		t.Fatal("empty process inventory envelope")
	}
}

func TestCommandRegistrationRefusesUndeclaredToolAtCatalogBoundary(t *testing.T) {
	names := []string{"process_list", "process_signal", "command", "verify", "command_output", "command_stop"}
	for missing, name := range names {
		t.Run(name, func(t *testing.T) {
			entries := map[string]toolschema.Entry{}
			for i := 0; i < missing; i++ {
				entries[names[i]] = toolschema.Entry{Description: "fixture", Schema: map[string]any{"type": "object"}}
			}
			reg, err := tools.NewCatalogRegistry(&toolschema.Config{Tools: entries})
			if err != nil {
				t.Fatal(err)
			}
			var cmd *command.CommandTool
			var verify *native.VerifyTool
			var output *command.CommandOutputTool
			var stop *command.CommandStopTool
			if err := registerCommandTools(reg, nil, nil, &cmd, &verify, &output, &stop); err == nil {
				t.Fatal("undeclared command tool registered")
			}
			if len(reg.List()) != missing {
				t.Fatalf("registered tools=%d want=%d", len(reg.List()), missing)
			}
			for _, meta := range reg.List() {
				if meta.Name == name {
					t.Fatal("missing metadata tool became callable")
				}
			}
		})
	}
}
