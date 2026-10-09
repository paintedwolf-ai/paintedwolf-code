package session

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCredentialAdvisoriesDistinguishWeakAndOtherLiterals(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	cases := []struct {
		name, tool, code string
		args             map[string]any
	}{
		{"nested account command", "command", "WEAK_CREDENTIAL_LITERAL", map[string]any{"command": "docker exec service gitea admin user create --password password"}},
		{"other credential literal", "command", "CREDENTIAL_LITERAL", map[string]any{"command": "example --client-secret=A123456789012345"}},
		{"structured tool credential", "external_tool", "WEAK_CREDENTIAL_LITERAL", map[string]any{"settings": map[string]any{"password": "password"}}},
		{"managed reference", "command", "", map[string]any{"command": "example --password={{paintedwolf-secret:123e4567-e89b-42d3-a456-426614174000}}"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: tc.name}, tc.tool, tc.args, "completed", 1, guidance.ToolResultFacts{})
			if !facts.Succeeded() {
				t.Fatalf("advisory changed outcome: %#v", facts)
			}
			if tc.code != "" && !facts.HasCode(tc.code) {
				t.Fatalf("missing %s: %s", tc.code, output)
			}
			if tc.code == "" && len(facts.Codes) > 0 {
				t.Fatalf("reference caused feedback: %#v", facts)
			}
		})
	}
}

func TestCredentialRemediesUseOfferedToolSnapshot(t *testing.T) {
	for _, offered := range []bool{false, true} {
		mgr, _ := newWeakSecretMintGuidanceManager(t)
		names := []string{}
		if offered {
			names = []string{"secret_generate", "ask_user"}
		}
		ctx := tools.WithRecoveryTools(t.Context(), names)
		output, facts := mgr.ToolPolicy.AfterTool(ctx, &api.Session{ID: "remedies"}, "command",
			map[string]any{"command": "example --password=password"}, "completed", 1, guidance.ToolResultFacts{})
		if !facts.HasCode("WEAK_CREDENTIAL_LITERAL") {
			t.Fatalf("credential advisory missing: %s", output)
		}
		for _, tool := range []string{"secret_generate", "ask_user"} {
			if strings.Contains(output, "`"+tool) != offered {
				t.Fatalf("remedy for %s disagrees with offered=%v: %s", tool, offered, output)
			}
		}
	}
}

func TestCredentialAdvisoriesKeepDistinctValueSubjects(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	_, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: "distinct-values"}, "write", map[string]any{"content": "MYSQL_PASSWORD=password\nPOSTGRES_PASSWORD=password1\n"}, "written", 1, guidance.ToolResultFacts{})
	result := guidance.ComposeToolResult("written", facts, nil)
	if len(result.Feedback) != 2 || result.Feedback[0].Subject == nil || result.Feedback[1].Subject == nil || result.Feedback[0].Subject.ID == result.Feedback[1].Subject.ID {
		t.Fatalf("credential subjects lost: %#v", result.Feedback)
	}
}

func TestCredentialRemediesDiscoverDeferredTools(t *testing.T) {
	for _, value := range []string{"password", "A123456789012345"} {
		for _, loaded := range []bool{false, true} {
			mgr, _ := newWeakSecretMintGuidanceManager(t)
			names := []string{"request_tools"}
			if loaded {
				names = append(names, "secret_generate", "ask_user")
			}
			ctx := tools.WithRecoveryTools(t.Context(), names)
			output, facts := mgr.ToolPolicy.AfterTool(ctx, &api.Session{ID: "deferred-remedies"}, "write",
				map[string]any{"content": "password=" + value}, "written", 1, guidance.ToolResultFacts{})
			if !facts.HasCode("WEAK_CREDENTIAL_LITERAL") && !facts.HasCode("CREDENTIAL_LITERAL") {
				t.Fatalf("credential observation lost: %s", output)
			}
			for _, tool := range []string{"secret_generate", "ask_user"} {
				if !strings.Contains(output, "`"+tool) {
					t.Fatalf("recoverable credential tool %s missing: %s", tool, output)
				}
			}
			if strings.Contains(output, "`request_tools`") == loaded {
				t.Fatalf("schema loading advice disagrees with loaded=%v: %s", loaded, output)
			}
			if strings.Contains(output, "report that need to the coordinating session") {
				t.Fatalf("reachable deferred tool was treated as unavailable: %s", output)
			}
		}
	}
}
