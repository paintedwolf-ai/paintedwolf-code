package session

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newPostToolGuidanceManager(t *testing.T) *Manager {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	catalogPath := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")
	testutil.FailErr(t, "install catalog", anchorcatalog.InstallFile(catalogPath))
	loader, err := oar.NewLoader(filepath.Join("..", "..", "..", "schemas"))
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "oar.LoadStock", err)
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolPost)
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	pipeline.EnableAnchor(oar.AnchorCredentialAssignment)
	mgr := &Manager{rejectFmt: rejectFmt}
	mgr.ensureCoordinatorRuntime()
	mgr.SetOARPipeline(pipeline, oar.NewRenderer(rejectFmt, nil))
	return mgr
}

var doomLoopWarnFloor = regexp.MustCompile(`paintedwolf\.repeat_count >= (\d+)`)

// doomLoopWarnAfter reads the repeat count DOOM_LOOP_REPEAT_WARN first fires at
// from the loaded policy unit.
func doomLoopWarnAfter(t *testing.T, mgr *Manager) int {
	t.Helper()
	for _, rule := range mgr.oarPipeline.Rules().All() {
		if rule.ID != "DOOM_LOOP_REPEAT_WARN" {
			continue
		}
		floor := doomLoopWarnFloor.FindStringSubmatch(rule.When)
		if floor == nil {
			t.Fatalf("DOOM_LOOP_REPEAT_WARN declares no repeat_count floor: when=%q", rule.When)
		}
		n, err := strconv.Atoi(floor[1])
		testutil.FailErr(t, "parse DOOM_LOOP_REPEAT_WARN floor", err)
		return n
	}
	t.Fatal("DOOM_LOOP_REPEAT_WARN rule missing")
	return 0
}

// TestDoomLoopWarnBannerFiresOnOARPath verifies that the production post-tool
// anchor carries the soft warning well before the hard block.
func TestDoomLoopWarnBannerFiresOnOARPath(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-1"}
	args := map[string]any{"path": "lycaon/internal/api/mcp_handlers.go", "pattern": "mcpProjectDir"}

	for count := doomLoopWarnAfter(t, mgr); count <= loopguard.DoomLoopMaxAttempts; count++ {
		out, _ := mgr.appendPostToolGuidance(context.Background(), sess, "grep", args, `{"matches":[]}`, count, guidance.ToolResultFacts{})
		if !strings.Contains(out, "DOOM_LOOP_REPEAT_WARN") {
			t.Fatalf("identical completion #%d carried no warn banner: %q", count, out)
		}
	}
}

func TestDoomLoopWarnForCommandOutputRoutesToProcessWait(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-1"}
	args := map[string]any{"handle": "cmd-docker-1", "cursor": 15395}

	out, _ := mgr.appendPostToolGuidance(
		context.Background(), sess, "command_output", args, `{"running":true,"next_cursor":15395}`,
		doomLoopWarnAfter(t, mgr), guidance.ToolResultFacts{},
	)
	for _, want := range []string{
		"DOOM_LOOP_REPEAT_WARN",
		`"handles":["cmd-docker-1"]`,
		`"kind":"process_done"`,
		"exact `next_cursor`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("command_output warning missing %q:\n%s", want, out)
		}
	}
}

func TestDoomLoopBlockForCommandOutputRoutesToProcessWait(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	mgr.oarPipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	reject, err := mgr.formatDoomLoopReject(
		context.Background(), "sess-1", "command_output",
		map[string]any{"handle": "cmd-docker-1", "cursor": 15395},
		loopguard.DoomLoopMaxAttempts, "",
	)
	testutil.FailErr(t, "format command_output doom block", err)
	if reject == nil {
		t.Fatal("command_output repetition did not render a block")
	}
	for _, want := range []string{
		"DOOM_LOOP_REPEAT",
		`"handles":["cmd-docker-1"]`,
		`"kind":"process_done"`,
		"terminal envelope",
	} {
		if !strings.Contains(reject.Body, want) {
			t.Fatalf("command_output block missing %q:\n%s", want, reject.Body)
		}
	}
}

// TestFruitlessSearchBannerFiresOnOARPath verifies that the third fruitless
// search for one question carries guidance even when the arguments differ.
func TestFruitlessSearchBannerFiresOnOARPath(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	guard := loopguard.NewMemoryDoomLoopGuard()
	mgr.SetDoomLoopGuard(guard)
	sess := &api.Session{ID: "sess-1"}
	ctx := context.Background()

	rewordings := []map[string]any{
		{"pattern": "hardenSpawn|reapProcessGroup", "path": "lycaon", "path_glob": "*.go"},
		{"pattern": "hardenSpawn|reapProcessGroup", "path": "lycaon"},
		{"pattern": "hardenSpawn|reapProcessGroup", "path": "lycaon", "include_hidden": true},
	}
	var out string
	for _, args := range rewordings {
		if _, err := guard.RecordSearchOutcome(ctx, sess.ID, "grep", args, false); err != nil {
			testutil.FailErr(t, "record fruitless", err)
		}
		out, _ = mgr.appendPostToolGuidance(ctx, sess, "grep", args, `{"matches":[]}`, 1, guidance.ToolResultFacts{})
	}
	if !strings.Contains(out, "DOOM_LOOP_FRUITLESS_SEARCH") {
		t.Fatalf("third fruitless rewording carried no banner: %q", out)
	}
}

// TestDoomLoopWarnBannerQuietBelowThreshold keeps the warn from firing on the
// first call of a pair — a single repeat is normal tool use, not a loop.
func TestDoomLoopWarnBannerQuietBelowThreshold(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-1"}
	args := map[string]any{"path": "README.md"}

	out, _ := mgr.appendPostToolGuidance(context.Background(), sess, "read", args, "body", 1, guidance.ToolResultFacts{})
	if strings.Contains(out, "DOOM_LOOP_REPEAT_WARN") {
		t.Fatalf("first attempt must not warn: %q", out)
	}
}

func TestRemotePackageDestinationBannerGivesNonWideningRecovery(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-package"}
	obs := confine.Observation{
		Applied: true, Network: confine.NetworkProxyOnly.Name(),
		DenialSubject: confine.DenialSubjectPackageHost,
		Destination:   "gitea.example.com",
		Signals:       []string{confine.SignalBoundaryRefused, confine.SignalRemotePackageDestinationDenied},
	}
	out, facts := mgr.appendPostToolGuidance(
		context.Background(), sess, "command", map[string]any{"command": "go run code.gitea.io/tea@v1.3.3"},
		`{"exit_code":1,"network_mode":"proxy_only"}`, 1, guidance.ToolResultFacts{Confine: obs},
	)
	for _, want := range []string{
		"Code: REMOTE_PACKAGE_EXECUTION_DESTINATION_DENIED",
		"gitea.example.com",
		"install or add the package",
		"later command",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("remote package guidance missing %q:\n%s", want, out)
		}
	}
	if !facts.HasCode(isolation.CodeRemotePackageDestinationDenied) {
		t.Fatalf("facts = %+v", facts)
	}
	if strings.Contains(out, "Code: SANDBOX_BOUNDARY_REFUSED") {
		t.Fatalf("remote package guidance offered contradictory recovery:\n%s", out)
	}
}

func TestVerifyUnverifiableBannerFiresOnConfineStop(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-verify"}
	obs := confine.StampRefusal("verify", sess.ID,
		confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
		confine.RefusalContext{MediatedNetwork: []confine.EgressHost{{Host: "blocked.test", Allowed: false}}},
	).Observation
	out, facts := mgr.appendPostToolGuidance(context.Background(), sess, "verify",
		map[string]any{"command": "check-service"},
		`{"outcome":"unverifiable","unverifiable_reason":"boundary_refused"}`, 1, guidance.ToolResultFacts{Confine: obs})
	if !strings.Contains(out, "Code: VERIFY_UNVERIFIABLE") {
		t.Fatalf("verify confine stop carried no banner:\n%s", out)
	}
	if strings.Contains(out, "Code: SANDBOX_BOUNDARY_REFUSED") || facts.HasCode(isolation.CodeBoundaryRefused) {
		t.Fatalf("verify uses VERIFY_UNVERIFIABLE, not the command last-resort:\n%s", out)
	}
	if !facts.HasCode("VERIFY_UNVERIFIABLE") {
		t.Fatalf("OAR must state VERIFY_UNVERIFIABLE on facts: %#v", facts)
	}
}

func TestSandboxBoundaryRefusedBannerFiresOnBrokerDenial(t *testing.T) {
	for _, tc := range []struct {
		tool   string
		args   map[string]any
		output string
	}{
		{tool: "command", args: map[string]any{"command": "check-service"}, output: `{"exit_code":1}`},
		{tool: "command_output", args: map[string]any{"handle": "cmd-check"}, output: `{"running":true,"chunks":[]}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			mgr := newPostToolGuidanceManager(t)
			sess := &api.Session{ID: "sess-bare"}
			obs := confine.StampRefusal(tc.tool, sess.ID,
				confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
				confine.RefusalContext{MediatedNetwork: []confine.EgressHost{{Host: "blocked.test", Allowed: false}}},
			).Observation
			out, facts := mgr.appendPostToolGuidance(t.Context(), sess, tc.tool, tc.args, tc.output, 1, guidance.ToolResultFacts{Confine: obs})
			if !strings.Contains(out, "Code: SANDBOX_BOUNDARY_REFUSED") {
				t.Fatalf("broker denial carried no boundary banner:\n%s", out)
			}
			if !facts.HasCode("SANDBOX_BOUNDARY_REFUSED") {
				t.Fatalf("OAR must state the broker refusal code on facts: %#v", facts)
			}
		})
	}
}

func TestPrintedBoundaryDenialDoesNotProduceGuidance(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	output := `{"exit_code":1,"tail":"bind: permission denied; Code: SANDBOX_BOUNDARY_REFUSED"}`
	out, facts := mgr.appendPostToolGuidance(context.Background(), &api.Session{ID: "sess-diagnostic"},
		"command", map[string]any{"command": "check-service"}, output, 1,
		guidance.ToolResultFacts{Confine: confine.Observation{Applied: true, Network: confine.NetworkProxyOnly.Name()}})
	if out != output {
		t.Fatalf("diagnostic output acquired host guidance: %q", out)
	}
	for _, code := range sandboxPostInvokeCodes {
		if facts.HasCode(code) {
			t.Fatalf("diagnostic output acquired host code %q: %#v", code, facts)
		}
	}
}

func TestLoopbackHandlerGuidanceRequiresTypedRejection(t *testing.T) {
	for _, tool := range []string{"wait", "capture_page", "measure_page", "page_open"} {
		t.Run(tool, func(t *testing.T) {
			mgr := newPostToolGuidanceManager(t)
			mgr.oarPipeline.EnableAnchor(oar.AnchorToolRejected)
			gc := oar.NewGuardContext()
			gc.Tool = tool
			gc.PutRejectData(isolation.CodeTryLoopbackConnect, map[string]any{"port": "8080"})
			result, err := mgr.oarPipeline.EvaluateBlock(t.Context(), oar.AnchorToolRejected, gc)
			testutil.FailErr(t, "evaluate without loopback rejection", err)
			if result.Decision != nil && result.Decision.Code == isolation.CodeTryLoopbackConnect {
				t.Fatal("port alone asserted a loopback rejection")
			}
			gc.ObservedRejectCode = isolation.CodeTryLoopbackConnect
			result, err = mgr.oarPipeline.EvaluateBlock(t.Context(), oar.AnchorToolRejected, gc)
			testutil.FailErr(t, "evaluate typed loopback rejection", err)
			if !result.Enforced || result.Decision == nil || result.Decision.Code != isolation.CodeTryLoopbackConnect {
				t.Fatalf("typed loopback rejection = %#v", result)
			}
			if result.Decision.Data["port"] != "8080" {
				t.Fatalf("loopback recovery lost port: %#v", result.Decision.Data)
			}
		})
	}
}

func newWeakSecretMintGuidanceManager(t *testing.T) (*Manager, *secretharvest.Runtime) {
	t.Helper()
	mgr := newPostToolGuidanceManager(t)
	ins, err := secretmint.LoadBundled()
	testutil.FailErr(t, "LoadBundled", err)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x11}, 32))
	testutil.FailErr(t, "NewFingerprinter", err)
	harvest := secretharvest.NewRuntime(fp)
	mgr.SetCredentialSlotProvider(func(context.Context, *api.Session) *secretmint.Inspector { return ins })
	mgr.SetSecretFingerprinter(fp)
	mgr.SetHarvestedFingerprint(func(root string, fp secretmatch.SecretFingerprint) bool {
		return harvest.Has(root, fp)
	})
	return mgr, harvest
}

func TestWeakSecretMintBannerFiresOnHtpasswd(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	sess := &api.Session{ID: "sess-mint"}
	out, facts := mgr.appendPostToolGuidance(context.Background(), sess, "command",
		map[string]any{"command": "htpasswd -b user password"},
		`{"exit_code":0}`, 1, guidance.ToolResultFacts{})
	if !strings.Contains(out, "Code: WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("mint command carried no banner:\n%s", out)
	}
	if !facts.HasCode("WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("OAR must state the code on facts: %#v", facts)
	}
}

func TestWeakSecretMintSilentOnUse(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	sess := &api.Session{ID: "sess-use"}
	out, _ := mgr.appendPostToolGuidance(context.Background(), sess, "command",
		map[string]any{"command": "mysql -p password"},
		`{"exit_code":0}`, 1, guidance.ToolResultFacts{})
	if strings.Contains(out, "WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("use-shaped mysql -p must stay silent:\n%s", out)
	}
}

func TestWeakSecretMintSilentOnHarvest(t *testing.T) {
	mgr, harvest := newWeakSecretMintGuidanceManager(t)
	sess := &api.Session{ID: "sess-harvest"}
	harvest.Harvest(secretharvest.ContainerRead{RootSessionID: sess.ID, Container: ".env", Content: []byte("MYSQL_PASSWORD=password\n")})
	out, _ := mgr.appendPostToolGuidance(context.Background(), sess, "command",
		map[string]any{"command": "htpasswd -b user password"},
		`{"exit_code":0}`, 1, guidance.ToolResultFacts{})
	if strings.Contains(out, "WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("harvested value is use, not mint:\n%s", out)
	}
}

func TestWeakSecretMintCounterSuppressesSecondIdenticalMint(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	sess := &api.Session{ID: "sess-dedup"}
	args := map[string]any{"command": "htpasswd -b user password"}
	first, _ := mgr.appendPostToolGuidance(context.Background(), sess, "command", args, `{"exit_code":0}`, 1, guidance.ToolResultFacts{})
	if !strings.Contains(first, "Code: WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("first mint must hint:\n%s", first)
	}
	second, _ := mgr.appendPostToolGuidance(context.Background(), sess, "command", args, `{"exit_code":0}`, 1, guidance.ToolResultFacts{})
	if strings.Contains(second, "WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("second identical mint must be counted only once:\n%s", second)
	}
}

func TestWeakSecretMintBannerFiresOnComposeWrite(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	sess := &api.Session{ID: "sess-write-mint"}
	out, facts := mgr.appendPostToolGuidance(context.Background(), sess, "write",
		map[string]any{"path": "compose.yaml", "content": "MYSQL_PASSWORD=password\n"},
		`{"ok":true}`, 1, guidance.ToolResultFacts{})
	if !strings.Contains(out, "Code: WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("compose write mint carried no banner:\n%s", out)
	}
	if !facts.HasCode("WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("OAR must state the code on facts: %#v", facts)
	}
}

func TestWeakSecretMintCounterSpansWriteThenEdit(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	sess := &api.Session{ID: "sess-write-edit-dedup"}
	first, _ := mgr.appendPostToolGuidance(context.Background(), sess, "write",
		map[string]any{"content": "MYSQL_PASSWORD=password\n"},
		`{"ok":true}`, 1, guidance.ToolResultFacts{})
	if !strings.Contains(first, "Code: WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("first write mint must hint:\n%s", first)
	}
	second, _ := mgr.appendPostToolGuidance(context.Background(), sess, "edit",
		map[string]any{"old_string": "x", "new_string": "MYSQL_PASSWORD=password\n"},
		`{"ok":true}`, 1, guidance.ToolResultFacts{})
	if strings.Contains(second, "WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("second edit of the same value must be counted only once:\n%s", second)
	}
}

func TestWeakSecretMintSilentWhenHarvestThenWrite(t *testing.T) {
	mgr, harvest := newWeakSecretMintGuidanceManager(t)
	sess := &api.Session{ID: "sess-harvest-write"}
	harvest.Harvest(secretharvest.ContainerRead{RootSessionID: sess.ID, Container: ".env", Content: []byte("MYSQL_PASSWORD=password\n")})
	out, _ := mgr.appendPostToolGuidance(context.Background(), sess, "write",
		map[string]any{"content": "MYSQL_PASSWORD=password\n"},
		`{"ok":true}`, 1, guidance.ToolResultFacts{})
	if strings.Contains(out, "WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("harvested value reappearing in write is use, not mint:\n%s", out)
	}
}

func TestWeakSecretMintCounterSpansCommandThenWrite(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	sess := &api.Session{ID: "sess-cmd-write-dedup"}
	first, _ := mgr.appendPostToolGuidance(context.Background(), sess, "command",
		map[string]any{"command": "htpasswd -b user password"},
		`{"exit_code":0}`, 1, guidance.ToolResultFacts{})
	if !strings.Contains(first, "Code: WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("command mint must hint:\n%s", first)
	}
	second, _ := mgr.appendPostToolGuidance(context.Background(), sess, "write",
		map[string]any{"content": "MYSQL_PASSWORD=password\n"},
		`{"ok":true}`, 1, guidance.ToolResultFacts{})
	if strings.Contains(second, "WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("same fingerprint on write must be counted only once:\n%s", second)
	}
}

func TestWeakSecretPolicyOwnsMeasurementThresholds(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	cases := []struct {
		value string
		warn  bool
	}{
		{"password", true},
		{"A12345678901234", true},
		{"A123456789012345", false},
		{"correct horse battery staple", false},
		{"word word word word word", true},
		{"lowercaseonlylongvalue", true},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			output, facts := mgr.appendPostToolGuidance(t.Context(), &api.Session{ID: tc.value}, "write", map[string]any{"content": "MYSQL_PASSWORD=" + tc.value + "\n"}, "written", 1, guidance.ToolResultFacts{})
			if facts.HasCode("WEAK_CREDENTIAL_LITERAL") != tc.warn {
				t.Fatalf("threshold for %q: warn=%v output=%s", tc.value, tc.warn, output)
			}
		})
	}
}

func TestFire5CredentialWarningsAreScopedBySessionAndFingerprint(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	args := map[string]any{"content": "MYSQL_PASSWORD=password\nPOSTGRES_PASSWORD=password1\n"}
	for _, sessionID := range []string{"first", "second"} {
		sess := &api.Session{ID: sessionID}
		output, _ := mgr.appendPostToolGuidance(t.Context(), sess, "write", args, "written", 1, guidance.ToolResultFacts{})
		if got := strings.Count(output, "Code: WEAK_CREDENTIAL_LITERAL"); got != 2 {
			t.Fatalf("[OAR-FIRE-5] %s warned %d times: %s", sessionID, got, output)
		}
		output, _ = mgr.appendPostToolGuidance(t.Context(), sess, "write", args, "written", 1, guidance.ToolResultFacts{})
		if strings.Contains(output, "WEAK_CREDENTIAL_LITERAL") {
			t.Fatalf("[OAR-FIRE-5] repeated credential warned again: %s", output)
		}
	}
}

func TestEval10CredentialMonitorDoesNotConsumeWarning(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	var rule *oar.Rule
	for _, candidate := range mgr.oarPipeline.Rules().All() {
		if candidate.ID == "WEAK_CREDENTIAL_LITERAL" {
			rule = candidate
		}
	}
	if rule == nil {
		t.Fatal("WEAK_CREDENTIAL_LITERAL rule missing")
	}
	sess := &api.Session{ID: "monitor"}
	args := map[string]any{"content": "MYSQL_PASSWORD=password\n"}
	rule.Enforcement = "monitor"
	output, _ := mgr.appendPostToolGuidance(t.Context(), sess, "write", args, "written", 1, guidance.ToolResultFacts{})
	if strings.Contains(output, "WEAK_CREDENTIAL_LITERAL") {
		t.Fatal("[OAR-EVAL-10] monitored warning was delivered")
	}
	rule.Enforcement = "enforce"
	output, _ = mgr.appendPostToolGuidance(t.Context(), sess, "write", args, "written", 1, guidance.ToolResultFacts{})
	if !strings.Contains(output, "Code: WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("[OAR-EVAL-10] monitor consumed warning: %s", output)
	}
}
