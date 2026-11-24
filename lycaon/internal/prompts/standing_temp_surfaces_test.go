package prompts

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStandingSharedTempSurfacesAcrossGrantAndPostureMatrix(t *testing.T) {
	root := configlayout.FindModuleRoot()
	surfaces := []string{
		"partials/command-surface.md",
		"partials/sandbox-confinement.md",
		"partials/temporary-storage.md",
		"agents/coordinator-core.md",
		"skill:run-a-throwaway-service",
		"skill:build-and-test-mobile-apps",
		"skill:run-browser-e2e-tests",
		"skill:analyze-local-data-files",
	}
	for _, platform := range []string{"linux", "darwin"} {
		for _, scratch := range []string{"", "/session/scratch"} {
			for _, readGranted := range [][]string{nil, {"/tmp"}} {
				for _, writeGranted := range [][]string{nil, {"/tmp"}} {
					for _, strict := range []bool{false, true} {
						for _, attached := range []bool{false, true} {
							for _, mutation := range []bool{false, true} {
								vars := map[string]any{
									"platform_os":                   platform,
									"profile_has_command":           true,
									"session_scratch_dir":           scratch,
									"shared_temp_readable":          len(readGranted) > 0,
									"shared_temp_read_locations":    readGranted,
									"shared_temp_writable":          len(writeGranted) > 0,
									"shared_temp_locations":         writeGranted,
									"reviews_confined_writes":       strict,
									"workspace_roots":               attached,
									"profile_command_mutation":      mutation,
									"profile_command_external_exec": mutation,
								}
								for _, ref := range surfaces {
									req := AuthorRenderRequest{
										ModuleRoot:  root,
										TemplateRef: ref,
										Vars:        vars,
									}
									if strings.HasPrefix(ref, "skill:") {
										req.TemplateRef = ""
										req.SkillName = strings.TrimPrefix(ref, "skill:")
									}
									res, err := AuthorRender(t.Context(), req)
									testutil.FailErr(t, "render "+ref, err)
									out := res.Output
									if strings.TrimSpace(out) == "" {
										t.Fatalf("empty output for %s under %+v", ref, vars)
									}
									if ref == "partials/temporary-storage.md" {
										if (scratch != "") != strings.Contains(out, "`@scratch` is this session’s private temporary directory") {
											t.Fatalf("scratch copy mismatch for %+v: %q", vars, out)
										}
										if (len(readGranted) > 0) != strings.Contains(out, "Read grants cover `/tmp`.") {
											t.Fatalf("read grant copy mismatch for %+v: %q", vars, out)
										}
										wantWrite := mutation && len(writeGranted) > 0
										if wantWrite != strings.Contains(out, "Write grants cover `/tmp`.") {
											t.Fatalf("write grant copy mismatch for %+v: %q", vars, out)
										}
										needApprovalCopy := len(readGranted) == 0 || (mutation && len(writeGranted) == 0)
										if needApprovalCopy != strings.Contains(out, "require approval") {
											t.Fatalf("approval copy mismatch for %+v: %q", vars, out)
										}
									}
									if ref == "partials/sandbox-confinement.md" && mutation {
										if (scratch != "") != strings.Contains(out, "Use `@scratch/<path>` for temporary files.") {
											t.Fatalf("sandbox scratch copy mismatch for %+v: %q", vars, out)
										}
										if (scratch == "") != strings.Contains(out, "Return temporary results inline.") {
											t.Fatalf("sandbox no-scratch fallback mismatch for %+v: %q", vars, out)
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
}
