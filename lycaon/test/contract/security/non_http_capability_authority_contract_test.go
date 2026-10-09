package contract

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Closed authority paths for structured non-HTTP capability requests.

func TestNonHTTPContractRawCapabilityRequestCannotReachConfineWithoutPermit(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/socket_spawn.go")
	fn := mustFindFunc(t, src, "socket_spawn.go", "ConfineRequestForSpawn")
	body := src[fn.Body.Pos()-1 : fn.Body.End()]
	if strings.Contains(body, "ParseCapabilityRequest") || strings.Contains(body, "capability_request") {
		t.Fatal("ConfineRequestForSpawn must not parse raw capability_request into SocketGrants")
	}
	if !strings.Contains(body, "FinalizeSocketGrantsForSpawn") || !strings.Contains(body, "FinalizeDirectIPForSpawn") {
		t.Fatal("ConfineRequestForSpawn must apply only host permits/task overlays via Finalize* helpers")
	}
}

func TestNonHTTPContractSocketAndDirectToolBoundaries(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sock := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/socket_capability.go")
	fn := mustFindFunc(t, sock, "socket_capability.go", "preflightSocketCapability")
	body := sock[fn.Body.Pos()-1 : fn.Body.End()]
	if !strings.Contains(body, "toolcontract.CapabilitySocket") || !strings.Contains(body, "toolcontract.CapabilityDirectIP") {
		t.Fatal("socket preflight must gate on typed socket and direct-IP capabilities")
	}
	if !strings.Contains(body, "isolation.CodeDirectIPRequestInvalid") {
		t.Fatal("socket-only invocations must reject direct_ip capability_request")
	}
	boundarySource := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/invocation_boundary.go")
	boundary := mustFindFunc(t, boundarySource, "invocation_boundary.go", "applyPreInvokeBoundary")
	boundaryBody := boundarySource[boundary.Body.Pos()-1 : boundary.Body.End()]
	if !strings.Contains(boundaryBody, "preflightSocketCapability") {
		t.Fatal("pre-invoke boundary must run socket capability preflight")
	}

	direct := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/direct_ip_capability.go")
	dfn := mustFindFunc(t, direct, "direct_ip_capability.go", "preflightDirectIPCapability")
	dbody := direct[dfn.Body.Pos()-1 : dfn.Body.End()]
	if !strings.Contains(dbody, "toolcontract.CapabilityDirectIP") {
		t.Fatal("direct IP preflight must gate on its typed capability")
	}

	command := contractcheck.ReadRepoFile(t, root, "lycaon/config/packs/painted-wolf/platform/tools/schemas/command.yaml")
	term := contractcheck.ReadRepoFile(t, root, "lycaon/config/packs/painted-wolf/platform/tools/schemas/terminal_open.yaml")
	verify := contractcheck.ReadRepoFile(t, root, "lycaon/config/packs/painted-wolf/platform/tools/schemas/verify.yaml")
	if !strings.Contains(command, "direct_ip") || !strings.Contains(command, "socket_paths") {
		t.Fatal("command schema must expose socket_paths and direct_ip")
	}
	if !strings.Contains(command, "socks_proxy:") {
		t.Fatal("command schema must expose top-level socks_proxy")
	}
	if !strings.Contains(term, "socket_paths") {
		t.Fatal("terminal_open schema must expose socket_paths")
	}
	if strings.Contains(term, "direct_ip") {
		t.Fatal("terminal_open schema must not expose direct_ip")
	}
	if !strings.Contains(term, "socks_proxy:") {
		t.Fatal("terminal_open schema must expose top-level socks_proxy")
	}
	if !strings.Contains(verify, "socks_proxy:") {
		t.Fatal("verify schema must expose top-level socks_proxy")
	}
}

func TestNonHTTPContractCurrentPermitsNonserializableAndCallBound(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	socketRT := contractcheck.ReadRepoFile(t, root, "lycaon/internal/session/approvalstate/socket_capability_runtime.go")
	if !strings.Contains(socketRT, "ConsumePermit") || !strings.Contains(socketRT, "LoadAndDelete") {
		t.Fatal("socket runtime must issue/consume current-call permits")
	}
	directRT := contractcheck.ReadRepoFile(t, root, "lycaon/internal/session/approvalstate/direct_ip_capability_runtime.go")
	if !strings.Contains(directRT, "LoadAndDelete") {
		t.Fatal("direct permit must be removed on single consumption")
	}
	if strings.Contains(socketRT, "type SocketPermit struct") || strings.Contains(directRT, "type DirectIPPermit struct") {
		t.Fatal("runtime must not retain dead exported permit DTOs")
	}
	finalize := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/direct_ip_capability.go")
	if !strings.Contains(finalize, "FinalizeDirectIPForSpawn") || !strings.Contains(finalize, "ConsumePermit") {
		t.Fatal("direct spawn must consume current-call permit")
	}
}

func TestNonHTTPContractTypedAuthResolvesOnlyMatchingSubstrate(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	g := confine.SocketGrant{ApprovedPath: "/tmp/a.sock", ResolvedPath: "/tmp/a.sock"}
	digest := confine.SocketPathsDigest([]confine.SocketGrant{g})

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	contractcheck.FailErr(t, "new approval store", err)
	approvals := settings.NewRuleApprovalGate(store, settings.NoSources())

	socketOnly := hitl.ProposedAction{
		Tool:       "command",
		ProjectDir: proj,
		Args:       map[string]any{"command": "true"},
		Contained: hitl.Contained{
			FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj},
			SocketPathsDigest: digest, SocketCount: 1,
		},
		SocketGrants:            []confine.SocketGrant{g},
		AuthorizedSocketDigests: []string{digest},
	}
	res, err := approvals.Evaluate(context.Background(), socketOnly)
	contractcheck.FailErr(t, "evaluate authorized socket", err)
	if !res.AutoApproved() {
		t.Fatalf("an authorized socket set must clear its own channel: %+v", res)
	}

	unauthSocket := socketOnly
	unauthSocket.AuthorizedSocketDigests = nil
	res, err = approvals.Evaluate(context.Background(), unauthSocket)
	contractcheck.FailErr(t, "evaluate unauthorized socket", err)
	if res.Gate() != api.GateUnobservedChannel {
		t.Fatalf("an unauthorized socket set must still ask: %+v", res)
	}

	direct := hitl.ProposedAction{
		Tool:       "command",
		ProjectDir: proj,
		Args:       map[string]any{"command": "true"},
		Contained: hitl.Contained{
			FSJailed: true, Egress: hitl.ContainedEgressDirectIP, DirectIP: true, Roots: []string{proj},
		},
	}
	res, err = approvals.Evaluate(context.Background(), direct)
	contractcheck.FailErr(t, "evaluate direct ip", err)
	if res.Gate() != api.GateUnobservedChannel {
		t.Fatalf("unmediated direct networking must ask: %+v", res)
	}

	directWithSocketAuth := direct
	directWithSocketAuth.AuthorizedSocketDigests = []string{digest}
	res, err = approvals.Evaluate(context.Background(), directWithSocketAuth)
	contractcheck.FailErr(t, "evaluate direct ip with socket auth", err)
	if res.Gate() != api.GateUnobservedChannel {
		t.Fatalf("socket authority must not resolve the direct-network axis: %+v", res)
	}

	authorizedDirect := direct
	authorizedDirect.AuthorizedDirectIP = true
	res, err = approvals.Evaluate(context.Background(), authorizedDirect)
	contractcheck.FailErr(t, "evaluate authorized direct ip", err)
	if !res.AutoApproved() {
		t.Fatalf("an authorized direct-network action must clear its own axis: %+v", res)
	}
}

func TestNonHTTPContractDirectAndExecutionSocketOfferScopes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	// Direct IP does not offer reusable approval scopes.
	direct := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/direct_ip_capability.go")
	for _, banned := range []string{
		"ApprovalGrantScopeChat", "ApprovalGrantScopeProject", "ApprovalGrantScopeDevice",
		"ApprovalGrantOffer{", "capability_scope_choices",
	} {
		if strings.Contains(direct, banned) {
			t.Fatalf("direct card must not offer %s", banned)
		}
	}

	sock := socketCapabilitySources(t, root)
	if !strings.Contains(sock, "hitl.TitleAllowForThisChat") {
		t.Fatal("execution socket card must offer chat scope")
	}
	if !strings.Contains(sock, "ApprovalGrantScopeChat") {
		t.Fatal("execution socket offers must be chat-scoped")
	}
	for _, banned := range []string{"ApprovalGrantScopeDevice", "capability_scope_choices"} {
		if strings.Contains(sock, banned) {
			t.Fatalf("execution socket card must not offer %s", banned)
		}
	}
	assertNoStandingSocketGrant(t)
}

func TestNonHTTPContractSettingsDurableSocketUsesSharedResolver(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/api/capabilityadmin/grant_create.go")
	resolveFn := mustFindFunc(t, src, "grant_create.go", "HandleResolveSocketGrant")
	createFn := mustFindFunc(t, src, "grant_create.go", "HandleCreateApprovalGrant")
	resolveBody := src[resolveFn.Body.Pos()-1 : resolveFn.Body.End()]
	createBody := src[createFn.Body.Pos()-1 : createFn.Body.End()]
	if !strings.Contains(resolveBody, "ResolveSocketRequest") || !strings.Contains(createBody, "ResolveSocketRequest") {
		t.Fatal("Settings resolve/create must share confine.ResolveSocketRequest")
	}
	if !strings.Contains(createBody, "socket_path must be an absolute") {
		t.Fatal("Settings create must require one exact absolute socket_path")
	}
	if strings.Contains(createBody, "socket_paths") {
		t.Fatal("Settings durable create must accept one exact socket, not a list")
	}
	if strings.Contains(createBody, "saved folder") {
		t.Fatal("project-scoped Settings create must not require a folder")
	}
	if !strings.Contains(createBody, "project scope requires project_id") {
		t.Fatal("project-scoped Settings create must require project_id")
	}
	if !strings.Contains(createBody, "ProjectID:") || !strings.Contains(createBody, "projectID") {
		t.Fatal("Settings create must persist project_id on the grant")
	}
}

func TestNonHTTPContractNeverAskStandsDownAsksKeepsValidationAndRecords(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	sock := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/socket_capability.go")
	fn := mustFindFunc(t, sock, "socket_capability.go", "preflightSocketCapability")
	body := sock[fn.Body.Pos()-1 : fn.Body.End()]
	if !strings.Contains(body, "approvalsDisabled") || !strings.Contains(body, "IssuePermit") {
		t.Fatal("never_ask must stand down socket cards by issuing a typed permit")
	}
	if !strings.Contains(body, "recordCapabilityDecision") || !strings.Contains(body, "AuthorizationSourceNeverAsk") {
		t.Fatal("never_ask socket path must still record capability decision")
	}
	if !strings.Contains(body, "ResolveCapabilitySockets") {
		t.Fatal("never_ask must not skip socket path validation/resolution")
	}

	direct := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/direct_ip_capability.go")
	dfn := mustFindFunc(t, direct, "direct_ip_capability.go", "preflightDirectIPCapability")
	dbody := direct[dfn.Body.Pos()-1 : dfn.Body.End()]
	if !strings.Contains(dbody, "approvalsDisabled") || !strings.Contains(dbody, "IssuePermit") {
		t.Fatal("never_ask must stand down direct cards by issuing a typed permit")
	}
	if !strings.Contains(dbody, "AuthorizationSourceNeverAsk") {
		t.Fatal("never_ask direct path must record AuthorizationSource never_ask")
	}

	egress := contractcheck.ReadRepoFile(t, root, "lycaon/internal/confine/confine_egress.go")
	ruleFn := mustFindFunc(t, egress, "confine_egress.go", "ruleOrPosture")
	ruleBody := egress[ruleFn.Body.Pos()-1 : ruleFn.Body.End()]
	denyIdx := strings.Index(ruleBody, "EgressRuleDeny")
	disableIdx := strings.Index(ruleBody, "approvalsDisabled")
	if denyIdx < 0 || disableIdx < 0 || denyIdx >= disableIdx {
		t.Fatal("egress deny rules must run before approval stand-down")
	}

	parse := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/capability_request.go")
	if !strings.Contains(parse, "ParseCapabilityRequest") || !strings.Contains(parse, "unsupported capability_request field") {
		t.Fatal("structured validation must remain independent of never_ask")
	}
}

func TestNonHTTPContractNoStderrProseProgramSocketNameInference(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	dirs := []string{
		filepath.Join(root, "lycaon", "internal", "tools"),
		filepath.Join(root, "lycaon", "internal", "session"),
		filepath.Join(root, "lycaon", "internal", "confine"),
	}
	banned := []string{
		"socket_denial_path",
		"ExtractSocket",
		"InferSocket",
		"retryOnStderr",
		"stderrSocket",
		"programNameFloor",
		"socketNameRisk",
	}
	err := filepath.WalkDir(filepath.Join(root, "lycaon"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		inScope := false
		for _, dir := range dirs {
			if strings.HasPrefix(path, dir+string(filepath.Separator)) || path == dir {
				inScope = true
				break
			}
		}
		if !inScope {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+"testdata"+string(filepath.Separator)) {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(b)
		for _, needle := range banned {
			if strings.Contains(src, needle) {
				t.Errorf("%s must not introduce %s", path, needle)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk capability packages", err)

	// Raw arguments do not authorize sockets.
	req := confine.Request{Roots: []string{t.TempDir()}}
	c := hitl.ContainedForRequest(req)
	if c.SocketCount != 0 || c.SocketPathsDigest != "" || c.DirectIP {
		t.Fatalf("default contained must not invent socket/direct from empty request: %+v", c)
	}
}

func mustFindFunc(t *testing.T, src, name, fnName string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	contractcheck.FailErr(t, "parse "+name, err)
	fn := findNamedFunc(file, fnName)
	if fn == nil || fn.Body == nil {
		t.Fatalf("%s missing", fnName)
	}
	return fn
}
