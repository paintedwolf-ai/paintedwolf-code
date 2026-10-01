package contract

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/usernotice"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// userNoticeHTTPOptionalSurface records HTTP notices emitted outside writeError.
var userNoticeHTTPOptionalSurface = map[string]string{
	"provider_not_configured":    "primary path is SSE session.host_error; http surface reserved for sync errors",
	"outbound_secret_denied":     "tool-reject facing notice; primary path is tool_approval + structured reject, not writeError",
	"GIT_INTERNALS_WRITE_DENIED": "tool-reject facing notice for native git-internals write sink",
	"GIT_SIGNING_UNSUPPORTED":    "tool/gitexec facing notice when repo policy requires signing",
	"GIT_REPO_CONFIG_UNSAFE":     "tool/gitexec facing notice when a repository config names programs git would run",
}

// userNoticeYAMLExtraRows records notices outside the API error ledger.
var userNoticeYAMLExtraRows = map[string]string{
	"session_aborted": "documented user_visible:false; host abort is not surfaced on notice rail",

	// Preflight notices bypass writeError.
	"OS_BELOW_FLOOR":              "preflight surface; emitted by the os_version probe",
	"CONFIG_DIR_UNWRITABLE":       "preflight surface; emitted by the config_dir probe",
	"HOST_FILE_LIMIT_REACHED":     "preflight surface; emitted by any probe whose open failed with EMFILE/ENFILE",
	"DISK_SPACE_LOW":              "preflight surface; emitted by the disk_space probe",
	"COMMAND_PATH_LIMITED":        "preflight surface; emitted by the command_path probe",
	"GIT_ENGINE_UNAVAILABLE":      "preflight surface; emitted by the git_engine probe",
	"GIT_INTERNALS_WRITE_DENIED":  "tool-reject facing notice for native git-internals write sink",
	"GIT_SIGNING_UNSUPPORTED":     "gitexec/tool facing notice when repo policy requires signing",
	"GIT_REPO_CONFIG_UNSAFE":      "gitexec/tool facing notice when a repository config names programs git would run",
	"SCANNER_ENGINE_UNAVAILABLE":  "preflight surface; emitted by the scanner_engine probe",
	"BROWSER_ENGINE_UNAVAILABLE":  "preflight surface; emitted by the browser_engine probe",
	"DECISION_ENGINE_UNAVAILABLE": "preflight surface; emitted by the decision_engine probe",
	"NO_PROVIDER_CONFIGURED":      "preflight surface; emitted by the provider_configured probe",
	"LITE_UNAVAILABLE":            "preflight surface; emitted by the lite_slot probe",

	// The outbound secret notice is rendered on approval cards.
	"outbound_secret_denied": "tool-reject / approval-card facing notice for outbound secret screen",
}

func loadUserNoticeConfig(t *testing.T) *usernotice.Config {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	cfg, err := usernotice.LoadNoticeDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	contractcheck.FailErr(t, "load host/user-notices", err)
	return cfg
}

func TestUserNoticeCodesHostErrorClosure(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg := loadUserNoticeConfig(t)

	mapped := scanAPIHostErrorCodes(t, filepath.Join(root, "lycaon", "internal", "api"))
	yamlHost := usernotice.HostErrorCodes(cfg)

	var violations []string
	for code := range mapped {
		entry, ok := cfg.UserNotices[code]
		if !ok {
			violations = append(violations, "promptHostErrorCode emits "+code+" but host/user-notices has no entry")
			continue
		}
		if !entry.IsUserVisible() {
			violations = append(violations, code+": promptHostErrorCode emits a user_visible:false entry")
			continue
		}
		if !entry.HasSurface("host_error") {
			violations = append(violations, code+": missing surfaces: [host_error] in host/user-notices")
		}
	}
	for _, code := range yamlHost {
		if !mapped[code] {
			violations = append(violations, code+": host_error surface in YAML but not emitted by promptHostErrorCode")
		}
	}
	contractcheck.FailViolations(t, "user notice host_error closure drift", violations)
}

func TestUserNoticeCodesWorkerFailureClosure(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg := loadUserNoticeConfig(t)

	mapped := scanWorkerExecuteFailureCodes(t, filepath.Join(root, "lycaon", "internal", "worker", "failure.go"))
	yamlWorker := usernotice.WorkerFailureCodes(cfg)

	var violations []string
	for code := range mapped {
		entry, ok := cfg.UserNotices[code]
		if !ok {
			violations = append(violations, "ExecuteFailureCode emits "+code+" but host/user-notices has no entry")
			continue
		}
		if !entry.IsUserVisible() {
			violations = append(violations, code+": ExecuteFailureCode emits a user_visible:false entry")
			continue
		}
		if !entry.HasSurface("worker_failure") {
			violations = append(violations, code+": missing surfaces: [worker_failure] in host/user-notices")
		}
	}
	for _, code := range yamlWorker {
		if !mapped[code] {
			violations = append(violations, code+": worker_failure surface in YAML but not emitted by ExecuteFailureCode")
		}
	}
	contractcheck.FailViolations(t, "user notice worker_failure closure drift", violations)
}

func TestUserNoticeCodesHTTPClosure(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg := loadUserNoticeConfig(t)
	apiDir := filepath.Join(root, "lycaon", "internal", "api")

	emitted := scanAPIErrorCodes(t, apiDir)
	// Codes other host packages hand to handlers as typed values.
	handedOver := scanTypedAPIErrorCodes(t, filepath.Dir(apiDir))
	yamlHTTP := usernotice.HTTPCodes(cfg)

	var violations []string
	for code := range emitted {
		entry, ok := cfg.UserNotices[code]
		if !ok {
			violations = append(violations, "writeError emits "+code+" but host/user-notices has no entry")
			continue
		}
		if !entry.IsUserVisible() {
			violations = append(violations, code+": writeError emits a user_visible:false entry")
			continue
		}
		if !entry.HasSurface("http") {
			violations = append(violations, code+": missing surfaces: [http] in host/user-notices")
		}
	}
	for _, code := range yamlHTTP {
		if emitted[code] || handedOver[code] {
			continue
		}
		if _, exempt := userNoticeHTTPOptionalSurface[code]; exempt {
			continue
		}
		violations = append(violations, code+": http surface in YAML but not emitted by writeError")
	}
	contractcheck.FailViolations(t, "user notice http closure drift", violations)
}

func TestUserNoticeLedgerDocumented(t *testing.T) {
	t.Parallel()
	cfg := loadUserNoticeConfig(t)
	declared := declaredAPIErrorCodes()

	var missing []string
	for code := range declared {
		if _, ok := cfg.UserNotices[code]; !ok {
			missing = append(missing, code)
		}
	}
	sort.Strings(missing)

	var orphans []string
	for code := range cfg.UserNotices {
		if _, ok := declared[code]; ok {
			continue
		}
		if _, ok := userNoticeYAMLExtraRows[code]; ok {
			continue
		}
		orphans = append(orphans, code)
	}
	sort.Strings(orphans)

	var violations []string
	for _, code := range missing {
		violations = append(violations, "ApiErrorCode vocabulary lists "+code+" but host/user-notices has no row")
	}
	for _, code := range orphans {
		violations = append(violations, "host/user-notices row "+code+" is not in the ApiErrorCode vocabulary")
	}
	contractcheck.FailViolations(t, "user notice ledger ↔ YAML drift", violations)
}

func TestSessionAbortedDocumentedNotUserVisible(t *testing.T) {
	cfg := loadUserNoticeConfig(t)
	entry, ok := cfg.UserNotices["session_aborted"]
	if !ok {
		t.Fatal("host/user-notices must document session_aborted with user_visible:false")
	}
	if entry.IsUserVisible() {
		t.Fatal("session_aborted must be user_visible:false")
	}
}

func TestAppBuildWiresUserNoticeCatalog(t *testing.T) {
	text := contractcheck.ServeWireSource(t)
	if !strings.Contains(text, "deps.UserNotices = ") {
		t.Fatal("serve build graph must hand the user notice catalog to the HTTP server's dependencies")
	}
	if !strings.Contains(text, "LoadEffectiveUserNotices") {
		t.Fatal("serve build graph must load the user notice catalog via LoadEffectiveUserNotices")
	}
}
