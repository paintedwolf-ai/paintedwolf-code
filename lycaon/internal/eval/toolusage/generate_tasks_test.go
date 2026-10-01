package toolusage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestLoadGenerateTasksRejectsDuplicatesAndUnknownPostures(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		testutil.FailErr(t, "write tasks", os.WriteFile(path, []byte(body), 0o600))
		return path
	}
	good := write("good.jsonl", `{"id":"a","prompt":"p","posture":"spec","workflow":"implement-dispatch","workflow_version":"2.3.4","follow_ups":[{"prompt":"more"}],"meta":{"k":1}}`+"\n")
	tasks, err := LoadGenerateTasks(good)
	testutil.FailErr(t, "load tasks", err)
	if len(tasks) != 1 || len(tasks[0].FollowUps) != 1 || tasks[0].Posture != wire.SessionPostureSpec || tasks[0].Workflow != "implement-dispatch" || tasks[0].WorkflowVersion != "2.3.4" {
		t.Fatalf("tasks = %+v", tasks)
	}
	for name, body := range map[string]string{
		"dup.jsonl":              `{"id":"a","prompt":"p"}` + "\n" + `{"id":"a","prompt":"q"}`,
		"posture.jsonl":          `{"id":"a","prompt":"p","posture":"relaxed"}`,
		"empty.jsonl":            "\n",
		"missing-version.jsonl":  `{"id":"a","prompt":"p","workflow":"implement-dispatch"}`,
		"missing-workflow.jsonl": `{"id":"a","prompt":"p","workflow_version":"2.3.4"}`,
	} {
		if _, err := LoadGenerateTasks(write(name, body)); err == nil {
			t.Errorf("%s loaded", name)
		}
	}
}
