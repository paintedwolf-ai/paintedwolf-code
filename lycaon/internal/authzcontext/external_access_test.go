package authzcontext_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildExternalAccess_roundTripAndSeal(t *testing.T) {
	ea := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{
		Endpoints: []authzcontext.ExternalAccessEndpointInput{
			{Host: "db.example.com", Port: 5432, Transport: "socks_tcp", Allowed: true},
			{Host: "db.example.com", Port: 5432, Transport: "socks_tcp", Allowed: true},
			{Host: "api.example.com", Port: 443, Transport: "http_connect", Allowed: false},
		},
		Sockets: []authzcontext.ExternalAccessSocketInput{
			{ApprovedPath: "/tmp/b.sock", ResolvedPath: "/tmp/b.sock", Scope: "chat"},
			{ApprovedPath: "/tmp/a.sock", ResolvedPath: "/tmp/a.sock", Scope: "current_action"},
		},
		DeclaredDestinations: []string{"db.example.com:5432?token=secret", "https://x.example/path?api_key=abc"},
		Direct:               true,
		Detections: []api.ExternalAccessDetection{{
			PackID: "pack", RuleID: "rule", Level: "high", ActionID: "call-1",
		}},
	})
	if ea == nil {
		t.Fatal("expected ExternalAccess")
	}
	if ea.VisibilitySummary != api.ExternalAccessVisibilitySummaryMixed {
		t.Fatalf("summary = %q want mixed", ea.VisibilitySummary)
	}
	if len(ea.Endpoints) != 2 {
		t.Fatalf("endpoints = %d want 2 after dedupe", len(ea.Endpoints))
	}
	var socksEP *api.ExternalAccessEndpoint
	for i := range ea.Endpoints {
		if ea.Endpoints[i].Transport == api.ExternalAccessTransportSocksTCP {
			socksEP = &ea.Endpoints[i]
		}
	}
	if socksEP == nil || socksEP.AttemptCount != 2 {
		t.Fatalf("socks endpoint attempts = %+v", socksEP)
	}
	if socksEP.Visibility != api.ExternalAccessVisibilityObserved {
		t.Fatalf("endpoint visibility = %q", socksEP.Visibility)
	}
	if len(ea.Sockets) != 2 {
		t.Fatalf("sockets = %d", len(ea.Sockets))
	}
	if ea.Sockets[0].ResolvedPath != "/tmp/a.sock" {
		t.Fatalf("socket order = %#v", ea.Sockets)
	}
	if ea.Sockets[0].ConnectionVisibility != api.ExternalAccessVisibilityUnobserved {
		t.Fatal("socket connection must stay unobserved")
	}
	if ea.Direct == nil || ea.Direct.ActualDestinationVisibility != api.ExternalAccessVisibilityUnobserved {
		t.Fatalf("direct = %+v", ea.Direct)
	}
	for _, d := range ea.DeclaredDestinations {
		if strings.Contains(d, "secret") || strings.Contains(d, "abc") {
			t.Fatalf("declared destination leaked secret: %q", d)
		}
	}
	for _, ep := range ea.Endpoints {
		for _, d := range ea.DeclaredDestinations {
			if ep.Host != "" && strings.Contains(d, ep.Host) && ep.Visibility == api.ExternalAccessVisibilityObserved {
				// declared strings may mention the same host; they must not be endpoint rows
				continue
			}
		}
	}

	raw, err := json.Marshal(authzcontext.EventDetail{ExternalAccess: ea})
	testutil.FailErr(t, "marshal detail", err)
	var round authzcontext.EventDetail
	if err := json.Unmarshal(raw, &round); err != nil {
		testutil.FailErr(t, "unmarshal detail", err)
	}
	if round.ExternalAccess == nil || round.ExternalAccess.VisibilitySummary != api.ExternalAccessVisibilitySummaryMixed {
		t.Fatalf("round-trip = %+v", round.ExternalAccess)
	}

	mem := authzcontext.NewMemoryStore()
	ledger := &authzcontext.Ledger{Store: mem}
	ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
		SessionID:  "sess-ea",
		Action:     authzcontext.EventActionCapabilityApplied,
		Outcome:    authzcontext.EventOutcomeAllowed,
		ResolvedBy: authzcontext.ResolvedByHuman,
		ToolName:   "command",
		Detail: authzcontext.DetailInput{
			Tool:                "command",
			AuthorizationSource: "never_ask",
			ExternalAccess:      ea,
		},
	})
	rows, err := mem.ListEvents(context.Background(), "sess-ea")
	testutil.FailErr(t, "list", err)
	if br := authzcontext.VerifyEvents("sess-ea", rows); br != nil {
		t.Fatalf("verify: %+v", br)
	}
}

func TestBuildExternalAccess_capsAndOrdering(t *testing.T) {
	sockets := make([]authzcontext.ExternalAccessSocketInput, 0, 12)
	for i := 0; i < 12; i++ {
		path := "/tmp/sock-" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".sock"
		if i < 10 {
			path = "/tmp/" + string(rune('a'+i)) + ".sock"
		}
		sockets = append(sockets, authzcontext.ExternalAccessSocketInput{
			ApprovedPath: path, ResolvedPath: path, Scope: "chat",
		})
	}
	ea := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{Sockets: sockets})
	if len(ea.Sockets) != authzcontext.MaxExternalAccessSockets {
		t.Fatalf("sockets capped = %d want %d", len(ea.Sockets), authzcontext.MaxExternalAccessSockets)
	}
	for i := 1; i < len(ea.Sockets); i++ {
		if ea.Sockets[i-1].ResolvedPath > ea.Sockets[i].ResolvedPath {
			t.Fatalf("sockets not sorted: %#v", ea.Sockets)
		}
	}
}

func TestBuildExternalAccess_declaredNeverObservedEndpoints(t *testing.T) {
	ea := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{
		DeclaredDestinations: []string{"db.example.com:5432"},
	})
	if len(ea.Endpoints) != 0 {
		t.Fatalf("declared must not become endpoints: %#v", ea.Endpoints)
	}
	if ea.VisibilitySummary != api.ExternalAccessVisibilitySummaryDeclared {
		t.Fatalf("summary = %q", ea.VisibilitySummary)
	}
}

func TestBuildExternalAccess_emptyUnknownAndAffirmativeNone(t *testing.T) {
	empty := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{})
	if empty.VisibilitySummary != api.ExternalAccessVisibilitySummaryUnknown {
		t.Fatalf("empty = %q want unknown", empty.VisibilitySummary)
	}
	none := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{AffirmativeNone: true})
	if none.VisibilitySummary != api.ExternalAccessVisibilitySummaryNone {
		t.Fatalf("none = %q", none.VisibilitySummary)
	}
	malformed := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{Malformed: true})
	if malformed.VisibilitySummary != api.ExternalAccessVisibilitySummaryUnknown {
		t.Fatalf("malformed = %q", malformed.VisibilitySummary)
	}
}

func TestEventDetailWithoutExternalAccessStillVerifies(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	ledger := &authzcontext.Ledger{Store: mem}
	ledger.AppendRecord(context.Background(), authzcontext.RecordInput{
		SessionID:  "sess-no-access",
		Action:     authzcontext.EventActionToolDenied,
		Outcome:    authzcontext.EventOutcomeDenied,
		ResolvedBy: authzcontext.ResolvedBySystemDeny,
		ToolName:   "command",
		Detail:     authzcontext.DetailInput{Tool: "command", RejectCode: "approval_denied"},
	})
	rows, err := mem.ListEvents(context.Background(), "sess-no-access")
	testutil.FailErr(t, "list", err)
	var detail authzcontext.EventDetail
	if err := json.Unmarshal([]byte(rows[0].DetailJSON), &detail); err != nil {
		testutil.FailErr(t, "unmarshal", err)
	}
	if detail.ExternalAccess != nil {
		t.Fatalf("detail must omit external_access, got %+v", detail.ExternalAccess)
	}
	if br := authzcontext.VerifyEvents("sess-no-access", rows); br != nil {
		t.Fatalf("verify: %+v", br)
	}
}
