package command

import (
	"bytes"
	"fmt"
	"slices"
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

func TestCommandCatalogRefusalsHaveShellVoice(t *testing.T) {
	catalog, err := guidance.LoadHintConfigStock()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for code, entry := range catalog.HintCodes {
		view := guidance.NormalizeRejectCodeView(code, entry)
		if view.Effect != "block" || !slices.Contains(view.Tools, "command") {
			continue
		}
		count++
		var stderr bytes.Buffer
		writeShellRefusal(&stderr, "command", "config.json", code, nil, map[string]any{
			"reason": "the requested operand is invalid", "expected_form": "a supported form",
			"source_line": 1, "source_column": 1, "path": "config.json",
		})
		line := stderr.String()
		if !strings.HasPrefix(line, "command: config.json: ") || !strings.HasSuffix(line, fmt.Sprintf(" [%s]\n", code)) {
			t.Errorf("%s stderr = %q", code, line)
		}
	}
	if count == 0 {
		t.Fatal("no command refusal entries inspected")
	}
	for code, word := range map[string]string{
		"SANDBOX_LOOPBACK_CONNECT_DENIED": "declined",
		"COMMAND_NATIVE_FORM_UNSUPPORTED": "does not provide",
		"GREP_MATCH_BUDGET": "limit",
	} {
		var stderr bytes.Buffer
		writeShellRefusal(&stderr, "command", "config.json", code, nil, nil)
		if !strings.Contains(strings.ToLower(stderr.String()), word) {
			t.Errorf("%s stderr = %q, want %q", code, stderr.String(), word)
		}
	}
}
