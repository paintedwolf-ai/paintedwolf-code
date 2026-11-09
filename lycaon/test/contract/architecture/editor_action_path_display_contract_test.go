package contract

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/wiring"
)

// AC-WR10-01: editor action prompts name the attached selection with the same
// canonical session-folder spelling as the attachment fence for the same
// reference — bare off the session's active root, @label/rel off any other
// attached root — and the read-only actions answer from the attached selection
// without redundant tool calls.
func TestEditorActionPathDisplayMatchesAttachmentFence(t *testing.T) {
	h := wiring.BuildForTest(t)
	baseURL := contractfixture.StartTestHTTPServer(t, h.Server)

	base := t.TempDir()
	appDir := filepath.Join(base, "app")
	libDir := filepath.Join(base, "lib")
	for _, dir := range []string{appDir, libDir} {
		contractcheck.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o750))
	}
	appSource := "package app\n\n// Run starts the loop.\nfunc Run() {}\n"
	libSource := "package lib\n\n// Parse decodes the payload.\n// It rejects truncated input.\nfunc Parse() {}\n"
	contractcheck.FailErr(t, "write app source", os.WriteFile(filepath.Join(appDir, "run.go"), []byte(appSource), 0o600))
	contractcheck.FailErr(t, "write lib source", os.WriteFile(filepath.Join(libDir, "parse.go"), []byte(libSource), 0o600))

	// Multi-root project: app is the primary root, lib a labeled attached root.
	projBody := fmt.Sprintf(`{"roots":[{"path":%q,"label":"app"},{"path":%q,"label":"lib"}]}`, appDir, libDir)
	projResp, err := contractfixture.AuthedHTTPPost(baseURL+"/v1/projects", "application/json", projBody)
	contractcheck.FailErr(t, "create project", err)
	defer func() { _ = projResp.Body.Close() }()
	if projResp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", projResp.StatusCode, string(contractfixture.ReadBody(t, projResp)))
	}
	var proj wire.Project
	testutil.FailErr(t, "decode project", json.NewDecoder(projResp.Body).Decode(&proj))
	libRootID := ""
	for _, root := range proj.Roots {
		if root.Label == "lib" {
			libRootID = root.ID
		}
	}
	if libRootID == "" {
		t.Fatalf("project has no lib root: %+v", proj.Roots)
	}

	sessResp, err := contractfixture.AuthedHTTPPost(baseURL+"/v1/sessions", "application/json",
		fmt.Sprintf(`{"project_id":%q,"posture":"build"}`, proj.ID))
	contractcheck.FailErr(t, "create session", err)
	defer func() { _ = sessResp.Body.Close() }()
	if sessResp.StatusCode != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", sessResp.StatusCode, string(contractfixture.ReadBody(t, sessResp)))
	}
	var sess wire.Session
	testutil.FailErr(t, "decode session", json.NewDecoder(sessResp.Body).Decode(&sess))
	sess = contractfixture.WaitSessionPrepared(t, baseURL, sess)
	if sess.WorkspaceRootID == "" || sess.WorkspaceRootID == libRootID {
		t.Fatalf("session active root = %q, want the primary app root", sess.WorkspaceRootID)
	}

	frame, err := h.Server.Admin.Extensions.Contributions.CaptureContributionFrame(t.Context(), proj.ID, appDir)
	contractcheck.FailErr(t, "capture contributions", err)
	actions := map[string]*contribution.Command{}
	for _, command := range frame.View.Contributions.Commands() {
		resolved, ok := frame.View.Contributions.ResolveCommand(command)
		if !ok || resolved.Action.Kind != contribution.ActionEditorAction {
			continue
		}
		switch {
		case strings.HasSuffix(command.ID, ":editor-verb-summarize"):
			actions["summarize"] = command
		case strings.HasSuffix(command.ID, ":editor-verb-explain"):
			actions["explain"] = command
		}
	}
	if len(actions) != 2 {
		t.Fatalf("editor actions missing from frame: found %d of summarize+explain", len(actions))
	}

	for _, tc := range []struct {
		name       string
		action     string
		rootID     string
		path       string
		wantPrompt string
		wantFence  string
	}{
		{
			name:       "summarize answers from attached selection on other root",
			action:     "summarize",
			rootID:     libRootID,
			path:       "parse.go",
			wantPrompt: "Give a concise overview of the code at @lib/parse.go lines 3–4.",
			wantFence:  "[User attached file: @lib/parse.go:3-4]",
		},
		{
			name:       "explain answers from attached selection on other root",
			action:     "explain",
			rootID:     libRootID,
			path:       "parse.go",
			wantPrompt: "Define and explain the code at @lib/parse.go lines 3–4 in its repository context.",
			wantFence:  "[User attached file: @lib/parse.go:3-4]",
		},
		{
			name:       "summarize on session folder stays bare",
			action:     "summarize",
			rootID:     sess.WorkspaceRootID,
			path:       "run.go",
			wantPrompt: "Give a concise overview of the code at run.go lines 3–4.",
			wantFence:  "[User attached file: run.go:3-4]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := actions[tc.action]
			invokeBody := fmt.Sprintf(
				`{"operation_id":%q,"frame_revision":%q,"context":{"path":%q,"root_id":%q,"start_line":3,"end_line":4}}`,
				uuid.NewString(), frame.Revision, tc.path, tc.rootID)
			invokeURL := baseURL + "/v1/sessions/" + sess.ID + "/commands/" + url.PathEscape(command.ID) + "/invoke"
			invokeResp, postErr := contractfixture.AuthedHTTPPost(invokeURL, "application/json", invokeBody)
			contractcheck.FailErr(t, "invoke editor action", postErr)
			defer func() { _ = invokeResp.Body.Close() }()
			if invokeResp.StatusCode != http.StatusAccepted {
				t.Fatalf("invoke status = %d body = %s", invokeResp.StatusCode, string(contractfixture.ReadBody(t, invokeResp)))
			}
			var accepted wire.CommandInvokeResponse
			testutil.FailErr(t, "decode invoke response", json.NewDecoder(invokeResp.Body).Decode(&accepted))
			if accepted.MessageID == "" {
				t.Fatal("invoke accepted without a message id")
			}

			contractfixture.WaitForSessionIdle(t, baseURL, sess.ID, 30*time.Second)
			msgs := contractfixture.ListMessagesAtURL(t, baseURL, sess.ID)
			var prompt *wire.Message
			for i := range msgs {
				if msgs[i].ID == accepted.MessageID {
					prompt = &msgs[i]
				}
			}
			if prompt == nil {
				t.Fatalf("admitted prompt %s missing from transcript", accepted.MessageID)
			}
			if !strings.Contains(prompt.Content, tc.wantPrompt) {
				t.Fatalf("prompt text missing canonical spelling %q:\n%s", tc.wantPrompt, prompt.Content)
			}
			if !strings.Contains(prompt.Content, tc.wantFence) {
				t.Fatalf("attachment fence missing %q:\n%s", tc.wantFence, prompt.Content)
			}
			// The read-only actions answer from the attached selection; the mock
			// model replies text-only, so any tool call means the prompt drove
			// redundant reads.
			for _, msg := range msgs {
				if msg.Role == wire.MessageRoleTool {
					t.Fatalf("turn issued a tool call: %+v", msg)
				}
				if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) > 0 {
					t.Fatalf("assistant issued tool calls: %+v", msg.ToolCalls)
				}
			}
		})
	}
}
