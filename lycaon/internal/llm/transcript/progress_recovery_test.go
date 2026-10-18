package transcript

import (
 "strings"
 "testing"

 "github.com/lycaon/lycaon/pkg/api"
)

func progressRecoveryFixture() []api.Message {
 return []api.Message{
  {ID:"request", Role:api.MessageRoleUser, Content:"Create the requested files"},
  {ID:"rejected-write", Role:api.MessageRoleTool, ToolResult:&api.ToolResult{
   Tool:"write", ToolCallID:"write-call", ToolArgs:map[string]any{"path":"temperature.py", "content":"print('hello')"},
   Invocation:&api.InvocationReceipt{ID:"write-receipt", Tool:"write", ToolCallID:"write-call", Status:api.InvocationStatusRejected, Failure:&api.InvocationFailure{Code:"PROGRESS_MISSING"}},
  }},
  {ID:"progress-result", Role:api.MessageRoleTool, ToolResult:&api.ToolResult{
   Tool:"update_progress", ToolCallID:"progress-call", Content:`{"status":"updated"}`,
   Invocation:&api.InvocationReceipt{ID:"progress-receipt", Tool:"update_progress", ToolCallID:"progress-call", Status:api.InvocationStatusCompleted, Invoked:true},
  }},
 }
}

func TestProgressRepairKeepsUnexecutedCallVisible(t *testing.T) {
 messages:=progressRecoveryFixture()
 projected:=Project(messages)
 got:=projected[len(projected)-1].Content
 for _, want:=range []string{"prior_unexecuted_calls", "rejected-write", "write-call", "temperature.py"} {
  if !strings.Contains(got,want) { t.Errorf("progress result lacks %q: %s",want,got) }
 }
 if strings.Contains(got,"print('hello')") { t.Fatal("reminder duplicated file contents") }
 if strings.Contains(messages[2].ToolResult.Content,"prior_unexecuted_calls") { t.Fatal("projection modified durable result") }
 again:=Project(projected)
 if again[len(again)-1].Content!=got { t.Fatal("projection is not idempotent") }
}
