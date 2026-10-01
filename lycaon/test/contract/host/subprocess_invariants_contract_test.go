package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// approvedHostLaunches is the closed inventory of host subprocess subjects and why each exists.
var approvedHostLaunches = map[string]string{
	"bundle_verification":              "inspects Apple code signatures and notarization tickets on packaged app bundles",
	"git_version_probe":                "verifies host git binary meets minimum engine version requirement",
	"decide_engine":                    "resident local decision engine (bialy) that ranks candidates and answers turn decisions",
	"ssh_agent_socket_lookup":          "retrieves SSH_AUTH_SOCK from launchd environment on headless macOS launches",
	"git":                              "executes Git CLI for worktree isolation, staging, signing, and repository management",
	"git_config_query":                 "reads global and system git configuration cascade",
	"git_identity_query":               "queries author identity configured in host git",
	"git_remote_query":                 "queries remote repository references",
	"git_repo_config_audit":            "audits repository-local git config for safety violations",
	"log_config_editor":                "spawns user interactive terminal editor ($EDITOR) for log configuration",
	"logview_pipe":                     "executes user-authored shell pipeline in the TUI log viewer",
	"document_extract_worker":          "spawns memory-isolated child process for PDF/DOCX document extraction",
	"release attestation verification": "runs GitHub CLI attestation verifier for signed releases",
	"library scanner worker":           "resident tree-sitter AST worker process for crash and leak containment",
	"user_path_probe":                  "probes interactive login shell to recover user PATH on GUI launch",
	"loopback_listener_probe":          "probes system listening ports and TCP socket process owners via lsof",
	"toolchain_version_probe":          "probes installed compilers and runtimes for version detection",
}

// forbiddenHostLaunchSubjects are host operations implemented natively in Go that must not shell out.
var forbiddenHostLaunchSubjects = map[string]string{
	"move selected file to operating system trash": "must use native desktoptrash package instead of osascript/gio/powershell",
	"browser_quarantine_clear":                     "must use native unix.Removexattr instead of xattr CLI",
	"macOS idle-sleep assertion":                   "must use native IOKit IOPMAssertion instead of caffeinate",
	"account_shell_lookup":                         "must use native getpwuid_r instead of dscacheutil",
}

// forbiddenCommandBinaries identifies external CLI binaries that must not be invoked for host operations.
var forbiddenCommandBinaries = map[string]string{
	"caffeinate":     "use native hostpower IOKit power assertion",
	"osascript":      "use native desktoptrash Foundation/Cgo binding",
	"gio":            "use native desktoptrash FreeDesktop spec",
	"powershell":     "use native desktoptrash Win32 SHFileOperationW",
	"powershell.exe": "use native desktoptrash Win32 SHFileOperationW",
	"dscacheutil":    "use native userpath getpwuid_r",
	"xattr":          "use native unix.Removexattr",
}

// TestHostSubprocessInventory limits host subprocesses to approvedHostLaunches
// and refuses CLIs whose operations have native implementations.
func TestHostSubprocessInventory(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	searchDirs := []string{
		filepath.Join(root, "lycaon", "internal"),
		filepath.Join(root, "lycaon", "cmd"),
	}

	fset := token.NewFileSet()
	var findings []string

	for _, baseDir := range searchDirs {
		err := filepath.WalkDir(baseDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			rel, _ := filepath.Rel(root, path)
			relSlash := filepath.ToSlash(rel)

			// These packages define the subprocess primitives.
			if strings.HasPrefix(relSlash, "lycaon/internal/exec/") || strings.HasPrefix(relSlash, "lycaon/internal/confine/") {
				return nil
			}

			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				if isCallToNamed(call.Fun, "HostLaunch") && len(call.Args) > 0 {
					if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						subject, err := strconv.Unquote(lit.Value)
						if err == nil {
							if reason, forbidden := forbiddenHostLaunchSubjects[subject]; forbidden {
								findings = append(findings, positionFinding(fset, call.Pos(), rel,
									"forbidden host subprocess subject "+strconv.Quote(subject)+": "+reason))
							} else if _, approved := approvedHostLaunches[subject]; !approved {
								findings = append(findings, positionFinding(fset, call.Pos(), rel,
									"unregistered host subprocess subject "+strconv.Quote(subject)+" — must use native Go or register in approvedHostLaunches"))
							}
						}
					}
				}

				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						val, err := strconv.Unquote(lit.Value)
						if err == nil {
							base := filepath.Base(val)
							if reason, forbidden := forbiddenCommandBinaries[base]; forbidden {
								findings = append(findings, positionFinding(fset, call.Pos(), rel,
									"forbidden external CLI invocation "+strconv.Quote(val)+": "+reason))
							}
						}
					}
				}

				return true
			})
			return nil
		})
		contractcheck.FailErr(t, "scan host subprocess inventory in "+baseDir, err)
	}

	if len(findings) != 0 {
		t.Fatalf("host subprocess inventory drift:\n  %s", strings.Join(findings, "\n  "))
	}
}

func isCallToNamed(expr ast.Expr, name string) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == name
	case *ast.SelectorExpr:
		return e.Sel.Name == name
	default:
		return false
	}
}
