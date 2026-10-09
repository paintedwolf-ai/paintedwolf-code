package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"github.com/lycaon/lycaon/internal/tools"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

type memoryDirectIPRuntime struct {
	permits map[string]struct {
		req, conf string
	}
	used map[string]bool
}

func (r *memoryDirectIPRuntime) IssuePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest string) {
	if r.permits == nil {
		r.permits = map[string]struct {
			req, conf string
		}{}
	}
	key := sessionID + "|" + toolCallID + "|" + actionDigest
	r.permits[key] = struct {
		req, conf string
	}{req: requestDigest, conf: confinementDigest}
}

// The spawn fixture holds no reusable authority, so only a permit authorizes it.
func (*memoryDirectIPRuntime) LeaseCovers(string, hitl.DirectIPLease) bool { return false }

func (*memoryDirectIPRuntime) GrantChat(string, hitl.DirectIPLease, string, string, *time.Time) {}

func (r *memoryDirectIPRuntime) ConsumePermit(sessionID, toolCallID, actionDigest, requestDigest, confinementDigest string) (bool, error) {
	key := sessionID + "|" + toolCallID + "|" + actionDigest
	if r.used == nil {
		r.used = map[string]bool{}
	}
	if r.used[key] {
		return false, nil
	}
	slot, ok := r.permits[key]
	if !ok {
		return false, nil
	}
	if slot.req != requestDigest || slot.conf != confinementDigest {
		return false, capabilityrequest.ErrString("direct IP permit mismatch")
	}
	r.used[key] = true
	return true, nil
}

func (r *memoryDirectIPRuntime) Authorized(sessionID, toolCallID, actionDigest string) bool {
	key := sessionID + "|" + toolCallID + "|" + actionDigest
	if r.used[key] {
		return false
	}
	_, ok := r.permits[key]
	return ok
}

func TestConfineRequestForSpawnSelectsDirectIP(t *testing.T) {
	dir := shortTempDir(t)
	rt := &memoryDirectIPRuntime{}
	digest := "action-digest"
	reqDigest := "req-digest"
	confDigest := "conf-digest"
	rt.IssuePermit("sess", "call-1", digest, reqDigest, confDigest)

	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess",
			ToolCallID: "call-1"},
		Direct: tools.InvocationDirect{DirectIPRequested: true,
			DirectIPAuthorized:        true,
			DirectIPActionDigest:      digest,
			DirectIPRequestDigest:     reqDigest,
			DirectIPConfineDigest:     confDigest,
			DirectIPCapabilityRuntime: rt},
		Local: tools.InvocationLocal{SocksProxyEnv: true},
	}
	req, reject := tools.ConfineRequestForSpawn(t.Context(), tctx, []string{dir})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if req.Egress != confine.EgressDirectIP {
		t.Fatalf("Egress = %v, want EgressDirectIP", req.Egress)
	}
	if req.SocksProxyEnv {
		t.Fatal("SocksProxyEnv must be false under direct IP")
	}
	_, reject = tools.ConfineRequestForSpawn(t.Context(), tctx, []string{dir})
	if reject == nil {
		t.Fatal("expected reject after permit consumption")
	}
}

func TestConfineRequestForSpawnSocksProxyEnv(t *testing.T) {
	dir := shortTempDir(t)
	req, reject := tools.ConfineRequestForSpawn(t.Context(), tools.ToolContext{
		Local: tools.InvocationLocal{SocksProxyEnv: true},
	}, []string{dir})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if !req.SocksProxyEnv {
		t.Fatal("SocksProxyEnv not plumbed onto confine.Request")
	}
	req2, reject := tools.ConfineRequestForSpawn(t.Context(), tools.ToolContext{}, []string{dir})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if req2.SocksProxyEnv {
		t.Fatal("default SocksProxyEnv must be false")
	}
}

func TestParseSocksProxyArg(t *testing.T) {
	ok, reject := capabilityrequest.ParseSocksProxyArg(nil)
	if reject != nil || ok {
		t.Fatalf("nil args = (%v, %v)", ok, reject)
	}
	ok, reject = capabilityrequest.ParseSocksProxyArg(map[string]any{"command": "echo"})
	if reject != nil || ok {
		t.Fatalf("absent = (%v, %v)", ok, reject)
	}
	ok, reject = capabilityrequest.ParseSocksProxyArg(map[string]any{"socks_proxy": true})
	if reject != nil || !ok {
		t.Fatalf("true = (%v, %v)", ok, reject)
	}
	ok, reject = capabilityrequest.ParseSocksProxyArg(map[string]any{"socks_proxy": false})
	if reject != nil || ok {
		t.Fatalf("false = (%v, %v)", ok, reject)
	}
	_, reject = capabilityrequest.ParseSocksProxyArg(map[string]any{"socks_proxy": "yes"})
	if reject == nil || reject.Code != "SANDBOX_SOCKS_PROXY_INVALID" {
		t.Fatalf("non-bool reject = %+v", reject)
	}
}

func TestFinalizeDirectIPForSpawnMismatch(t *testing.T) {
	rt := &memoryDirectIPRuntime{}
	rt.IssuePermit("sess", "call-1", "action", "req", "conf")
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess",
			ToolCallID: "call-1"},
		Direct: tools.InvocationDirect{DirectIPRequested: true,
			DirectIPActionDigest:      "action",
			DirectIPRequestDigest:     "req-OTHER",
			DirectIPConfineDigest:     "conf",
			DirectIPCapabilityRuntime: rt},
	}
	reject := tools.FinalizeDirectIPForSpawn(tctx)
	if reject == nil {
		t.Fatal("expected mismatch reject")
	}
	if reject.Code != "SANDBOX_DIRECT_IP_AUTHORIZATION_CHANGED" {
		t.Fatalf("reject code = %q", reject.Code)
	}
}

func TestDirectProcessEnvironmentStripsProxyKeys(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"HTTP_PROXY=http://127.0.0.1:9",
		"https_proxy=http://127.0.0.1:9",
		"ALL_PROXY=socks5h://127.0.0.1:9",
		"HOME=/tmp",
	}
	got := confine.ProcessEnvironment(base, confine.Confinement{Network: confine.NetworkDirectIP})
	joined := ""
	for _, e := range got {
		joined += e + "\n"
		if stringsHasProxyKey(e) {
			t.Fatalf("direct process environment retained proxy key: %s", e)
		}
	}
	if !containsEnv(got, "PATH=/bin") || !containsEnv(got, "HOME=/tmp") {
		t.Fatalf("direct process environment lost non-proxy keys: %s", joined)
	}
}

func TestExternalAccessRequiresAppliedDirectIP(t *testing.T) {
	previousBuilder := tools.ExternalAccessBuilder
	t.Cleanup(func() { tools.ExternalAccessBuilder = previousBuilder })

	var captured tools.ExternalAccessBuildInput
	tools.ExternalAccessBuilder = func(in tools.ExternalAccessBuildInput) *api.ExternalAccess {
		captured = in
		return &api.ExternalAccess{}
	}
	out := &tools.ToolInvocationOut{}
	tctx := tools.ToolContext{
		Direct: tools.InvocationDirect{DirectIPRequested: true,
			DirectIPDeclared: []string{"api.example"}},
		Effects: tools.InvocationEffects{Out: out},
	}

	tools.CaptureExternalAccess(tctx, nil, false)
	if out.ExternalAccess != nil {
		t.Fatal("a request without applied direct IP must not be reported as direct access")
	}

	tools.CaptureExternalAccess(tctx, nil, true)
	if out.ExternalAccess == nil || !captured.Direct {
		t.Fatal("applied direct IP must be reported")
	}
	if len(captured.DeclaredDestinations) != 1 || captured.DeclaredDestinations[0] != "api.example" {
		t.Fatalf("declared destinations = %v", captured.DeclaredDestinations)
	}
}

func stringsHasProxyKey(entry string) bool {
	for _, key := range confine.ProxyEnvKeys() {
		if strings.HasPrefix(entry, key+"=") {
			return true
		}
	}
	return false
}

func containsEnv(entries []string, want string) bool {
	for _, e := range entries {
		if e == want {
			return true
		}
	}
	return false
}
