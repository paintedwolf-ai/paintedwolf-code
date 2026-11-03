//go:build integration

package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCredentialFileScreensGrepAndCommandWithoutRead(t *testing.T) {
	for _, name := range []string{"grep", "command"} {
		t.Run(name, func(t *testing.T) {
			const value = "shapeless-person-value-9988"
			files := credentialFilesFixture(t)
			root := t.TempDir()
			testutil.FailErr(t, "seed command project", files.queries.CreateProject(t.Context(), db.CreateProjectParams{ID: testdbseed.DefaultProjectID, CreatedAt: "2026-01-01T00:00:00Z", LastOpenedAt: "2026-01-01T00:00:00Z"}))
			testutil.FailErr(t, "attach command root", files.queries.CreateProjectRoot(t.Context(), db.CreateProjectRootParams{ID: "r1", ProjectID: testdbseed.DefaultProjectID, Path: root, Label: "root", IsPrimary: 1, AddedAt: "2026-01-01T00:00:00Z", Kind: "attached"}))
			testutil.FailErr(t, "write credential file", os.WriteFile(filepath.Join(root, ".env"), []byte("DB_PASSWORD="+value), 0600))
			testutil.FailErr(t, "admit attached root", files.EnsureProject(t.Context(), testdbseed.DefaultProjectID))
			boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{"grep": true, "command": true, "http_request": true}}})
			tc := tools.ToolContext{Identity: tools.InvocationIdentity{ProjectID: testdbseed.DefaultProjectID, SessionID: "chat", RootSessionID: "chat", Agent: "implement"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: root, IsPrimary: true}}, ActiveRootID: "r1"}, Effects: tools.InvocationEffects{CredentialFiles: files}}
			var output string
			var err error
			if name == "grep" {
				output, err = (&survey.GrepTool{Boundary: boundary}).Run(t.Context(), map[string]any{"path": ".env", "pattern": "DB_PASSWORD", "include_hidden": true, "max_matches": 10}, tc)
			} else {
				output, err = credentialCommandOutput(t, files, root)
			}
			testutil.FailErr(t, "deliver "+name, err)
			if !strings.Contains(output, value) {
				t.Fatalf("%s did not deliver fixture value: %s", name, output)
			}
			matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
			testutil.FailErr(t, "build matcher", err)
			matcher.SetFingerprinter(files.fp)
			matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
				var values []secretmatch.HarvestedValue
				for _, value := range files.harvest.FileValues(testdbseed.DefaultProjectID) {
					values = append(values, secretmatch.HarvestedValue{Secret: value.Secret(), Name: value.Name, RuleID: value.RuleID, Source: value.Source})
				}
				return values
			})
			cards := 0
			screen := llm.NewModelSecretScreen(matcher, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
				cards++
				return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
			})
			request, err := screen.Screen(t.Context(), llm.ScreenDestination{ID: "provider"}, modelcall.CompletionRequest{Debug: modelcall.RequestDebug{ProjectID: testdbseed.DefaultProjectID, SessionID: "chat"}, Messages: []api.Message{{Role: api.MessageRoleTool, Content: output}}})
			testutil.FailErr(t, "screen provider request", err)
			if cards != 1 || strings.Contains(request.Messages[0].Content, value) {
				t.Fatalf("provider cards=%d content=%q", cards, request.Messages[0].Content)
			}
			outboundCards := 0
			registry := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register HTTP tool", httpaction.Register(registry, httpaction.Deps{Boundary: boundary, SecretMatcher: matcher, SecretAsk: func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
				outboundCards++
				return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
			}}))
			_, err = registry.Run(t.Context(), "http_request", map[string]any{"url": "https://recipient.example/", "method": "POST", "body_text": value}, tc)
			var refusal *toolrejection.ToolReject
			if outboundCards != 1 || !errors.As(err, &refusal) || refusal.Code != toolrejection.OutboundSecretDeniedCode {
				t.Fatalf("outbound cards=%d error=%v", outboundCards, err)
			}
		})
	}
}
