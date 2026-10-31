package command

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
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
	testutil.FailErr(t, "guidance.LoadHintConfigStock failed", err)
	language, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "toolcontract.LanguageV1 failed", err)
	nativeNames := make(map[string]bool, len(language.Commands))
	for _, command := range language.Commands {
		nativeNames[command.Name] = true
	}
	count := 0
	for code, entry := range catalog.HintCodes {
		view := guidance.NormalizeRejectCodeView(code, entry)
		nativeOperation := false
		for _, operation := range entry.Operations() {
			nativeOperation = nativeOperation || nativeNames[operation]
		}
		if view.Effect != "block" || (!slices.Contains(view.Tools, "command") && !nativeOperation) {
			continue
		}
		count++
		var stderr bytes.Buffer
		writeShellRefusal(&stderr, "command", "config.json", code, nil, map[string]any{
			"reason": "the requested operand is invalid", "expected_form": "a supported form",
			"source_line": 1, "source_column": 1, "path": "config.json",
		})
		line := stderr.String()
		prefix, suffix := "command: config.json: ", fmt.Sprintf(" [%s]\n", code)
		reason := strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) || strings.TrimSpace(reason) == "" || strings.Count(line, "\n") != 1 {
			t.Errorf("%s stderr = %q", code, line)
		}
		stderr.Reset()
		writeShellRefusal(&stderr, "command", "-s", code, nil, map[string]any{"reason": "invalid operand", "expected_form": "a supported form"})
		if !strings.HasPrefix(stderr.String(), "command: request: ") {
			t.Errorf("%s option subject = %q", code, stderr.String())
		}
	}
	if count == 0 {
		t.Fatal("no command refusal entries inspected")
	}
	for code, word := range map[string]string{
		"SANDBOX_LOOPBACK_CONNECT_DENIED": "declined",
		"COMMAND_NATIVE_FORM_UNSUPPORTED": "run this form",
		"GREP_MATCH_BUDGET":               "limit",
	} {
		var stderr bytes.Buffer
		writeShellRefusal(&stderr, "command", "config.json", code, nil, nil)
		if !strings.Contains(strings.ToLower(stderr.String()), word) {
			t.Errorf("%s stderr = %q, want %q", code, stderr.String(), word)
		}
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
