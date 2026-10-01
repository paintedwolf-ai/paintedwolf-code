package providerwire

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	explicitPolicy = providerprofile.PromptCachePolicy{
		Profile: "anthropic", Mode: providerprofile.PromptCacheExplicitBreakpoints,
		Lifetime: providerprofile.PromptCacheLifetime{
			Standing: providerprofile.CacheDuration(time.Hour), History: providerprofile.CacheDuration(5 * time.Minute),
		},
	}
	automaticPolicy = providerprofile.PromptCachePolicy{
		Profile: "router", Mode: providerprofile.PromptCacheAutomaticPrefix, SessionKey: true, AffinityHeader: "x-session-id",
	}
)

func tieredMessages() []api.Message {
	return []api.Message{
		{Role: api.MessageRoleSystem, Content: "stable", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleTool, Content: "result", PromptCacheBreakpoint: api.PromptCacheTierHistory},
		{Role: api.MessageRoleSystem, Content: "volatile"},
	}
}

// Each boundary carries the lifetime its tier takes; the request-level
// marker covers the growing tail and takes the history lifetime.
func TestProjectPromptCacheExplicitBreakpointsCarryTierLifetimes(t *testing.T) {
	proj := ProjectPromptCache(modelcall.CompletionRequest{Messages: tieredMessages()}, explicitPolicy, modelinfo.ModelCapabilities{})
	want := []PromptCacheBreakpoint{
		{Index: 0, Tier: api.PromptCacheTierStanding, Lifetime: time.Hour},
		{Index: 2, Tier: api.PromptCacheTierHistory, Lifetime: 5 * time.Minute},
	}
	if len(proj.Breakpoints) != 2 || proj.Breakpoints[0] != want[0] || proj.Breakpoints[1] != want[1] {
		t.Fatalf("breakpoints = %+v, want %+v", proj.Breakpoints, want)
	}
	if !proj.RequestMarker || proj.RequestLifetime != 5*time.Minute {
		t.Fatalf("request marker = %v %v", proj.RequestMarker, proj.RequestLifetime)
	}
}

func TestLifetimeTokenSpellsWireLifetimes(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "", 5 * time.Minute: "5m", time.Hour: "1h", 30 * time.Minute: "30m", 90 * time.Minute: "1h30m"} {
		if got := LifetimeToken(d); got != want {
			t.Fatalf("LifetimeToken(%v) = %q, want %q", d, got, want)
		}
	}
}

// A transport with fewer slots keeps the boundaries nearest the growing tail.
func TestTrailingPromptCacheBreakpointsKeepsTheTail(t *testing.T) {
	all := []PromptCacheBreakpoint{{Index: 1}, {Index: 4}, {Index: 7}, {Index: 9}}
	if got := TrailingPromptCacheBreakpoints(all, 3); len(got) != 3 || got[0].Index != 4 || got[2].Index != 9 {
		t.Fatalf("trailing 3 = %v", got)
	}
	if got := TrailingPromptCacheBreakpoints(all, 8); len(got) != 4 {
		t.Fatalf("within limit = %v", got)
	}
	if got := TrailingPromptCacheBreakpoints(all, 0); got != nil {
		t.Fatalf("zero limit = %v", got)
	}
	set := PromptCacheBreakpointSet([]PromptCacheBreakpoint{{Index: -1}, {Index: 2, Tier: api.PromptCacheTierHistory}})
	if _, ok := set[-1]; ok || set[2].Tier != api.PromptCacheTierHistory {
		t.Fatalf("set = %v", set)
	}
}

// Merging the preamble keeps the last boundary inside it.
func TestSystemPreambleKeepsPromptCacheBreakpoint(t *testing.T) {
	out := SystemPreamble([]api.Message{
		{Role: api.MessageRoleSystem, Content: "core"},
		{Role: api.MessageRoleSystem, Content: "procedures", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleUser, Content: "go"},
	})
	if len(out) != 2 || out[0].PromptCacheBreakpoint != api.PromptCacheTierStanding || out[1].PromptCacheBreakpoint != api.PromptCacheTierNone {
		t.Fatalf("preamble marker lost: %+v", out)
	}
}

// An automatic-prefix route sends the session key and affinity header, and a
// per-part marker only where the model's rule gives one.
func TestProjectPromptCacheAutomaticFollowsTheModelRule(t *testing.T) {
	marker := providerprofile.PromptCacheMarkerCacheControl
	requestMarker := true
	providerprofile.SetPromptCacheRules([]providerprofile.PromptCacheRule{
		{Profiles: []string{"router"}, Match: []string{"anthropic/"}, Marker: &marker, RequestMarker: &requestMarker},
	})
	t.Cleanup(func() { providerprofile.SetPromptCacheRules(nil) })
	req := modelcall.CompletionRequest{Model: "anthropic/claude-sonnet-5", Messages: tieredMessages(), Debug: modelcall.RequestDebug{SessionID: "sess-or"}}
	proj := ProjectPromptCache(req, automaticPolicy, modelinfo.ModelCapabilities{})
	if proj.PromptCacheKey != "sess-or" || proj.SessionAffinityHeader != "x-session-id" || proj.SessionAffinityValue != "sess-or" {
		t.Fatalf("routing = %+v", proj)
	}
	if proj.Marker != providerprofile.PromptCacheMarkerCacheControl || !proj.RequestMarker || len(proj.Breakpoints) != 2 || proj.Breakpoints[0].Lifetime != 0 {
		t.Fatalf("markers = %+v", proj)
	}
	req.Model = "deepseek/deepseek-v4"
	if proj := ProjectPromptCache(req, automaticPolicy, modelinfo.ModelCapabilities{}); proj.Marker != providerprofile.PromptCacheMarkerNone || len(proj.Breakpoints) != 0 || proj.PromptCacheKey != "sess-or" {
		t.Fatalf("unmatched model = %+v", proj)
	}
}

// The catalog marker sends cache_control only when the route reports prompt
// caching, whatever the model id says; LiteLLM translates it upstream.
func TestProjectPromptCacheCatalogMarkerReadsTheRouteFact(t *testing.T) {
	policy := providerprofile.PromptCachePolicy{Profile: "litellm", Mode: providerprofile.PromptCacheAutomaticPrefix, Marker: providerprofile.PromptCacheMarkerCatalog}
	req := modelcall.CompletionRequest{Model: "claude-alias", Messages: tieredMessages(), Debug: modelcall.RequestDebug{SessionID: "sess-lite"}}
	supported := modelinfo.ModelCapabilities{PromptCaching: modelinfo.Evidence(modelinfo.CapabilitySupported, "litellm")}
	proj := ProjectPromptCache(req, policy, supported)
	if proj.Marker != providerprofile.PromptCacheMarkerCacheControl || proj.PromptCacheKey != "" || proj.SessionAffinityHeader != "" {
		t.Fatalf("supported route = %+v", proj)
	}
	if proj := ProjectPromptCache(req, policy, modelinfo.ModelCapabilities{}); proj.Marker != providerprofile.PromptCacheMarkerNone || len(proj.Breakpoints) != 0 {
		t.Fatalf("no catalog fact must send nothing: %+v", proj)
	}
}

func TestProjectPromptCacheLocalAndNone(t *testing.T) {
	req := modelcall.CompletionRequest{Messages: tieredMessages()}
	local := providerprofile.PromptCachePolicy{Mode: providerprofile.PromptCacheLocalKV, KeepAlive: "24h"}
	if proj := ProjectPromptCache(req, local, modelinfo.ModelCapabilities{}); proj.KeepAlive != "24h" || len(proj.Breakpoints) != 0 {
		t.Fatalf("local = %+v", proj)
	}
	if proj := ProjectPromptCache(req, providerprofile.PromptCachePolicy{}, modelinfo.ModelCapabilities{}); proj.KeepAlive != "" || len(proj.Breakpoints) != 0 || proj.RequestMarker {
		t.Fatalf("unattached policy sent controls: %+v", proj)
	}
}
