package hitl_test

import (
	"math"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGrantKeyIsOpaque(t *testing.T) {
	secret := "cargo-token-that-must-not-be-persisted"
	key := hitl.GrantKey(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "cargo publish --token " + secret},
},
Scope: hitl.ActionScope{
ProjectDir: "/tmp/project",
},
})
	if !strings.HasPrefix(key, "action_") || strings.Contains(key, secret) || strings.Contains(key, "cargo publish") {
		t.Fatalf("GrantKey exposed action content: %q", key)
	}
}

func directIPAction(args map[string]any) hitl.ProposedAction {
	return hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: args,
},
Scope: hitl.ActionScope{
ProjectDir: "/tmp/proj",
},
Execution: hitl.ActionExecution{
Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressDirectIP, Roots: []string{"/tmp/proj"}},
},
}
}

// A lease card promises re-ask when "the command, its declared destinations, or
// the confinement changes". Capture framing, deadlines, and the order the model
// listed its destinations in are none of those.
func TestGrantKeyIgnoresAuthorityNeutralArgs(t *testing.T) {
	capReq := func(dests ...string) map[string]any {
		list := make([]any, 0, len(dests))
		for _, d := range dests {
			list = append(list, d)
		}
		return map[string]any{"direct_ip": map[string]any{"declared_destinations": list}}
	}
	base := directIPAction(map[string]any{
		"command":            ".venv/bin/python3 rate_service.py",
		"timeout_ms":         120000,
		"capability_request": capReq("https://open.er-api.com:443", "https://api.frankfurter.dev:443"),
	})
	want := hitl.GrantKey(base)

	cases := map[string]map[string]any{
		"terminal capture added": {
			"command":            ".venv/bin/python3 rate_service.py",
			"timeout_ms":         120000,
			"terminal_capture":   map[string]any{"caption": "Exchange rate cache service demo", "winsize": map[string]any{"cols": 120, "rows": 40}},
			"capability_request": capReq("https://open.er-api.com:443", "https://api.frankfurter.dev:443"),
		},
		"timeout changed": {
			"command":            ".venv/bin/python3 rate_service.py",
			"timeout_ms":         20000,
			"capability_request": capReq("https://open.er-api.com:443", "https://api.frankfurter.dev:443"),
		},
		"wait window added": {
			"command":            ".venv/bin/python3 rate_service.py",
			"timeout_ms":         120000,
			"wait_ms":            5000,
			"capability_request": capReq("https://open.er-api.com:443", "https://api.frankfurter.dev:443"),
		},
		"destinations reordered": {
			"command":            ".venv/bin/python3 rate_service.py",
			"timeout_ms":         120000,
			"capability_request": capReq("https://api.frankfurter.dev:443", "https://open.er-api.com:443"),
		},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if got := hitl.GrantKey(directIPAction(args)); got != want {
				t.Fatalf("grant identity changed on an authority-neutral edit\n want %q\n  got %q", want, got)
			}
		})
	}
}

// The same canonicalization must not blur anything the boundary depends on.
func TestGrantKeySeparatesAuthorityBearingArgs(t *testing.T) {
	base := directIPAction(map[string]any{"command": "curl https://example.org", "timeout_ms": 1000})
	want := hitl.GrantKey(base)
	cases := map[string]map[string]any{
		"different command":   {"command": "curl https://evil.example", "timeout_ms": 1000},
		"cwd added":           {"command": "curl https://example.org", "timeout_ms": 1000, "cwd": "sub"},
		"env added":           {"command": "curl https://example.org", "timeout_ms": 1000, "env": map[string]any{"TOKEN": "x"}},
		"background added":    {"command": "curl https://example.org", "timeout_ms": 1000, "background": true},
		"socks proxy added":   {"command": "curl https://example.org", "timeout_ms": 1000, "socks_proxy": true},
		"stdout redirect":     {"command": "curl https://example.org", "timeout_ms": 1000, "stdout_to": "out.txt"},
		"destination widened": {"command": "curl https://example.org", "timeout_ms": 1000, "capability_request": map[string]any{"direct_ip": true}},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if got := hitl.GrantKey(directIPAction(args)); got == want {
				t.Fatalf("grant identity survived an authority-bearing edit: %q", got)
			}
		})
	}
}

func TestGrantIdentityRefusesUnencodableArguments(t *testing.T) {
	for name, value := range map[string]any{"channel": make(chan int), "nan": math.NaN()} {
		t.Run(name, func(t *testing.T) {
			action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "echo ok", "invalid": value},
},
Scope: hitl.ActionScope{
SessionID: "session",
},
}
			key := hitl.GrantKey(action)
			if key != "" {
				t.Fatalf("unencodable action has usable grant key %q", key)
			}
			if offer := hitl.ExactActionOffer(action); offer.ID != "" || offer.Grant.ID != "" {
				t.Fatalf("unencodable action minted offer: %+v", offer)
			}
			if offer := hitl.ExactActionSetOffer(action, []string{"valid-key", key}); offer.ID != "" {
				t.Fatalf("unencodable member minted action-set offer: %+v", offer)
			}
			decision := &gate.Decision{Primary: api.GateExplicitApprovalRequest, ReasonKey: "explicit_approval_request:constant"}
			if hitl.DecisionFullyQuieted(decision, nil, key, func(string) bool { return true }) {
				t.Fatal("unencodable action reused quiet authority")
			}
			if options := hitl.QuietOptions(action, decision, nil, nil); len(options) != 0 {
				t.Fatal("unencodable action offered quiet authority")
			}
			plan, err := hitl.NewApprovalPlan(action, "", hitl.ApprovalSubject{}, hitl.ApprovalPresentation{}, nil, nil, hitl.FaceContext{})
			if err == nil || plan != nil || !strings.Contains(err.Error(), "identity cannot be encoded") {
				t.Fatalf("invalid identity reached plan construction: plan=%+v err=%v", plan, err)
			}
		})
	}
}

func TestGrantKeyPreservesEncodedIdentity(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "echo ok"},
},
Scope: hitl.ActionScope{
ProjectDir: "/tmp/project",
},
}
	const want = "action_aPs33LLXNi1xSHjl-xyDpdMYzbxBlOQuC7LtctsIUDM"
	if got := hitl.GrantKey(action); got != want {
		t.Fatalf("valid action identity changed: got %q want %q", got, want)
	}
}
