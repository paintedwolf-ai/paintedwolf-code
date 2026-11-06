package command

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestRedirectRefusalKeepsPolicyCopyOutOfChildStderr(t *testing.T) {
	var stderr bytes.Buffer
	reject := &toolrejection.ToolReject{Code: "PROGRESS_MISSING"}
	refusal := guidance.NewRefusal(reject.Code, "Rejected: update the checklist\nCode: PROGRESS_MISSING").
		WithPolicyCopy(map[string]string{"cause": "Project file edits need a checklist."}).WithCause(reject)
	(&ProgramDispatcher{}).reportRefusal(&stderr, commandprogram.RefusalReport{
		Operation: ">", Operand: "config.json", Code: reject.Code, Cause: refusal,
	})
	if got := stderr.String(); got != ">: config.json: Project file edits need a checklist [PROGRESS_MISSING]\n" {
		t.Fatalf("stderr = %q", got)
	}
	if strings.Contains(stderr.String(), "Rejected:") {
		t.Fatal("structured OAR block replaced shell stderr")
	}
}

func TestHostRefusalUsesTypedURLInsteadOfOption(t *testing.T) {
	var stderr bytes.Buffer
	writeRejectRefusal(commandprogram.Streams{Error: &stderr}, "curl", &toolrejection.ToolReject{Code: "SANDBOX_LOOPBACK_CONNECT_DENIED", Data: map[string]any{"url": "http://127.0.0.1:8080"}})
	if got := stderr.String(); got != "curl: http://127.0.0.1:8080: The person declined this connection to the local service [SANDBOX_LOOPBACK_CONNECT_DENIED]\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestHostRefusalDoesNotTreatOptionAsSubject(t *testing.T) {
	var stderr bytes.Buffer
	writeRejectRefusal(commandprogram.Streams{Error: &stderr}, "sed", &toolrejection.ToolReject{Code: "COMMAND_OPTION_UNSUPPORTED", Data: map[string]any{"word": "-z", "expected_form": "sed [-n] SCRIPT [FILE...]"}})
	if got := stderr.String(); !strings.HasPrefix(got, "sed: request: ") {
		t.Fatalf("stderr = %q", got)
	}
}
