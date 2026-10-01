package confine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const testTag = "0123456789abcdef0123456789abcdef"

func kernelMessage(report string) string {
	return report + "\n" + refusalTagPrefix + testTag
}

// The kernel's report grammar yields process, operation, and target; the rule
// message names the action.
func TestParseKernelRefusalReadsTheReportGrammar(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		want    kernelRefusal
	}{
		{"unix bind", kernelMessage("Sandbox: limactl(4121) deny(1) network-bind /Users/p/.colima/_lima/_networks/user-v2/user-v2_fd.sock"),
			kernelRefusal{PID: 4121, Process: "limactl", Operation: "network-bind", Target: "/Users/p/.colima/_lima/_networks/user-v2/user-v2_fd.sock", Count: 1}},
		{"duplicates", kernelMessage("3 duplicate reports for Sandbox: Python(86295) deny(1) file-write-create /tmp/ro/x"),
			kernelRefusal{PID: 86295, Process: "Python", Operation: "file-write-create", Target: "/tmp/ro/x", Count: 3}},
		{"one duplicate", kernelMessage("1 duplicate report for Sandbox: node(7) deny(1) network-outbound remote:*:443"),
			kernelRefusal{PID: 7, Process: "node", Operation: "network-outbound", Target: "remote:*:443", Count: 1}},
		{"parenthesized process name", kernelMessage("Sandbox: com.example (helper)(99) deny(1) signal"),
			kernelRefusal{PID: 99, Process: "com.example (helper)", Operation: "signal", Count: 1}},
		{"target naming a verdict", kernelMessage("Sandbox: sh(5) deny(1) file-write-data /tmp/a) deny(1) b"),
			kernelRefusal{PID: 5, Process: "sh", Operation: "file-write-data", Target: "/tmp/a) deny(1) b", Count: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseKernelRefusal(tc.message)
			tc.want.Tag = testTag
			if !ok || got != tc.want {
				t.Fatalf("parse = %+v ok=%v, want %+v", got, ok, tc.want)
			}
		})
	}
}

// A report the kernel cut at its size limit keeps its process for
// attribution and drops the grant its partial target cannot name.
func TestTruncatedReportNamesItsProcessNotAGrant(t *testing.T) {
	long := "/Users/p/" + strings.Repeat("d", 40)
	got, ok := parseKernelRefusal("Sandbox: touch(812) deny(1) file-write-create " + long + truncatedReportSuffix)
	if !ok || !got.Truncated || got.Tag != "" || got.PID != 812 || got.Target != long {
		t.Fatalf("truncated report = %+v ok=%v", got, ok)
	}
	rules, _ := projectRules(t)
	if r := routeRefusal(rules, got.Operation, got.Target, true); r.Recovery != RecoverNone || r.Grant != "" || r.Target != long+truncatedReportSuffix {
		t.Fatalf("truncated write routed to %+v", r)
	}
	if r := routeRefusal(rules, "network-bind", long, true); r.Recovery != RecoverHostExecution {
		t.Fatalf("truncated unix listener routed to %+v", r)
	}
}

// A report no tagged rule wrote names no action.
func TestParseKernelRefusalRequiresTheActionTag(t *testing.T) {
	for _, message := range []string{
		"Sandbox: sh(5) deny(1) file-write-data /tmp/a",
		"Sandbox: sh(5) deny(1) file-write-data /tmp/a\nsome other rule message",
		"Sandbox: sh(5) deny(1) file-write-data /tmp/a\n" + refusalTagPrefix + "not-hex",
		"sandboxd: sh(5) deny(1) file-write-data /tmp/a\n" + refusalTagPrefix + testTag,
	} {
		if got, ok := parseKernelRefusal(message); ok {
			t.Fatalf("parsed %q as %+v", message, got)
		}
	}
}

func streamJSON(t *testing.T, ev streamEvent) []byte {
	t.Helper()
	line, err := json.Marshal(ev)
	testutil.FailErr(t, "encode stream event", err)
	return line
}

// Only the kernel's sandbox extension writes refusal reports, and only this
// process writes the host's markers.
func TestParseStreamLineAuthenticatesTheWriter(t *testing.T) {
	report := kernelMessage("Sandbox: sh(5) deny(1) file-write-data /tmp/a")
	kernel := streamEvent{
		EventType: "logEvent", EventMessage: report, ProcessImagePath: "/kernel",
		SenderImagePath: "/System/Library/Extensions" + sandboxSenderSuffix,
	}
	if line, ok := parseStreamLine(streamJSON(t, kernel), 42); !ok || line.refusal.Tag != testTag {
		t.Fatalf("kernel report not delivered: %+v", line)
	}
	forged := kernel
	forged.ProcessImagePath, forged.SenderImagePath = "/usr/bin/logger", "/usr/bin/logger"
	if line, ok := parseStreamLine(streamJSON(t, forged), 42); ok {
		t.Fatalf("a userspace message passed as a kernel report: %+v", line)
	}

	mark := streamEvent{EventType: "logEvent", EventMessage: refusalMarkPrefix + "n1", ProcessID: 42}
	if line, ok := parseStreamLine(streamJSON(t, mark), 42); !ok || line.mark != "n1" {
		t.Fatalf("own marker not delivered: %+v", line)
	}
	if line, ok := parseStreamLine(streamJSON(t, mark), 43); ok {
		t.Fatalf("another process's marker was accepted: %+v", line)
	}
	if line, ok := parseStreamLine(streamJSON(t, streamEvent{EventType: "lossEvent"}), 42); !ok || !line.lost {
		t.Fatalf("dropped messages went unreported: %+v", line)
	}
	if _, ok := parseStreamLine([]byte(`Filtering the log data using "..."`), 42); ok {
		t.Fatal("the stream's banner parsed as an event")
	}
}

func projectRules(t *testing.T) (FilesystemRules, string) {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	project, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project", err)
	rules, err := filesystemRules(Confinement{Roots: []string{project}})
	testutil.FailErr(t, "build rules", err)
	return rules, project
}

// A refused path takes the layer the rendered rules place it in, and that
// layer's recovery names the grant; a path the rules admit has no recovery.
func TestFilesystemRefusalsFollowTheRenderedRules(t *testing.T) {
	rules, project := projectRules(t)
	configDir := os.Getenv("LYCAON_CONFIG_DIR")
	for _, tc := range []struct {
		operation, target string
	}{
		{"file-write-create", "/opt/example-tool/cache/state.json"},
		{"file-write-data", filepath.Join(project, "AGENTS.md")},
		{"file-write-unlink", filepath.Join(configDir, "store.db")},
		{"file-read-data", filepath.Join(configDir, "api.token")},
		{"file-write-data", filepath.Join(project, "main.go")},
	} {
		got := routeRefusal(rules, tc.operation, tc.target, false)
		verdict := rules.Verdict(operationAccess(tc.operation), tc.target)
		switch {
		case verdict.Allowed:
			if got.Recovery != RecoverNone || got.Layer != "" {
				t.Fatalf("%s %s: admitted path routed to %+v", tc.operation, tc.target, got)
			}
		case verdict.Layer.Recovery().Terminal():
			if got.Recovery != RecoverNone || got.Grant != "" || got.Layer != verdict.Layer {
				t.Fatalf("%s %s: terminal layer %s routed to %+v", tc.operation, tc.target, verdict.Layer, got)
			}
		default:
			want := verdict.Layer.Recovery()
			if string(got.Recovery) != want.Capability || got.Grant != want.GrantPath(tc.target, rules.roots...) {
				t.Fatalf("%s %s: routed to %+v, want %s %s", tc.operation, tc.target, got, want.Capability, want.GrantPath(tc.target, rules.roots...))
			}
		}
	}
	if got := routeRefusal(rules, "file-write-create", "/opt/example-tool/cache/state.json", false); got.Recovery != RecoverWriteRoot || got.Grant != "/opt/example-tool/cache" {
		t.Fatalf("outside write roots routed to %+v", got)
	}
}

// Creating a skill asks once for the tree it lands in; editing one asks for
// the file.
func TestRefusedAgentPolicyNamesTheLoaderTreeGrant(t *testing.T) {
	rules, project := projectRules(t)
	skills := filepath.Join(project, ".agents", "skills")
	existing := filepath.Join(skills, "review", "SKILL.md")
	testutil.FailErr(t, "create skill", os.MkdirAll(filepath.Dir(existing), 0o755))
	for _, tc := range []struct {
		name, target, grant string
	}{
		{"edit an existing skill", existing, existing},
		{"create a skill", filepath.Join(skills, "ship", "SKILL.md"), skills},
		{"instructions", filepath.Join(project, "docs", "AGENTS.md"), filepath.Join(project, "docs", "AGENTS.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := routeRefusal(rules, "file-write-create", tc.target, false)
			if got.Recovery != RecoverWriteRoot || got.Grant != tc.grant {
				t.Fatalf("routed to %+v, want write_root %s", got, tc.grant)
			}
		})
	}
}

// Each operation outside the filesystem routes to the capability whose rule
// admits it; no capability admits a unix-socket listener.
func TestNetworkAndProcessRefusalsRouteToTheirCapability(t *testing.T) {
	rules, _ := projectRules(t)
	for _, tc := range []struct {
		operation, target string
		recovery          RefusalRecovery
		grant, port       string
	}{
		{"network-outbound", "/var/run/docker.sock", RecoverSocketPath, "/var/run/docker.sock", ""},
		{"network-outbound", "remote:*:5432", RecoverOutboundPort, "", "5432"},
		{"network-bind", "local:*:8080", RecoverLocalListen, "", "8080"},
		{"network-bind", "/Users/p/.colima/_lima/_networks/user-v2/user-v2_fd.sock", RecoverHostExecution, "", ""},
		{"signal", "", RecoverProcessControl, "", ""},
		{"iokit-open", "AppleVirtIOService", RecoverHostExecution, "", ""},
		{"mach-lookup", "com.apple.system.powerd", RecoverHostExecution, "", ""},
	} {
		got := routeRefusal(rules, tc.operation, tc.target, false)
		if got.Recovery != tc.recovery || got.Grant != tc.grant || got.Port() != tc.port {
			t.Fatalf("%s %s routed to %+v (port %q), want %s %q port %q",
				tc.operation, tc.target, got, got.Port(), tc.recovery, tc.grant, tc.port)
		}
	}
}

// Every deny rule of a bound confinement carries the action's tag, so no
// refusal the profile makes goes unattributed; an unbound one carries none.
func TestBoundProfileTagsEveryDenyRule(t *testing.T) {
	_, project := projectRules(t)
	c := Confinement{Roots: []string{project}, Network: NetworkDeny, refusalTag: testTag}
	profile, err := BuildProfile(c)
	testutil.FailErr(t, "build tagged profile", err)
	tag := `(with message "` + refusalTagPrefix + testTag + `")`
	blocks := strings.Split(profile, "\n(")
	denies := 0
	for _, block := range blocks {
		if !strings.HasPrefix(strings.TrimPrefix(block, "("), "deny ") {
			continue
		}
		denies++
		if !strings.Contains(block, tag) {
			t.Fatalf("deny rule carries no action tag:\n(%s", block)
		}
	}
	if denies < 2 {
		t.Fatalf("profile rendered %d deny rules:\n%s", denies, profile)
	}

	c.refusalTag = ""
	untagged, err := BuildProfile(c)
	testutil.FailErr(t, "build untagged profile", err)
	if strings.Contains(untagged, "with message") {
		t.Fatalf("unbound profile carries a tag:\n%s", untagged)
	}
}
