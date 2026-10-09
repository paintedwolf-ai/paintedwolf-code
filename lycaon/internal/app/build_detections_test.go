package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBuildDetectionObservationBeforeSettingsAndAcrossReloads(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the full app graph with shipped config")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	app, err := Build(t.Context(), testBuildConfig(t, configlayout.FindModuleRoot()))
	testutil.FailErr(t, "build app", err)
	t.Cleanup(func() { _ = app.Close() })
	testdbseed.InsertProjectRoot(t, app.DB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := app.Sessions.Manager.CreateForProject(t.Context(), testdbseed.DefaultProjectID, wire.SessionPostureBuild)
	testutil.FailErr(t, "create observation session", err)
	var remembered []secretmatch.Remembered
	app.Sessions.Manager.SetRememberSecrets(func(_ string, values []secretmatch.Remembered) {
		remembered = append(remembered, values...)
	})
	const token = "67ff6e39282cb4d81f8da08b44df3e8b524a5960"
	for _, step := range []struct {
		name, method, path, body string
		want                     int
	}{
		{name: "startup", want: 1},
		{"settings read", http.MethodGet, "/v1/detection-packs", "", 1},
		{"disable", http.MethodPatch, "/v1/detection-packs/credential-minting", `{"enabled":false}`, 0},
		{"enable", http.MethodPatch, "/v1/detection-packs/credential-minting", `{"enabled":true}`, 1},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.path != "" {
				req := httptest.NewRequestWithContext(t.Context(), step.method, step.path, strings.NewReader(step.body))
				req.Header.Set("Authorization", "Bearer test-token")
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				app.Server.ServeHTTP(response, req)
				if response.Code != http.StatusOK {
					t.Fatalf("settings response = %d: %s", response.Code, response.Body.String())
				}
			}
			remembered = nil
			app.Sessions.Manager.ObserveMintedCredential(t.Context(), sess, "command",
				map[string]any{"command": "gitea admin user generate-access-token --username fixture"}, token)
			if len(remembered) != step.want {
				t.Fatalf("remembered %d values, want %d", len(remembered), step.want)
			}
			for _, value := range remembered {
				if value.Secret != token || value.RuleID == "" {
					t.Fatal("minted value lost its content or provenance")
				}
			}
		})
	}
}
