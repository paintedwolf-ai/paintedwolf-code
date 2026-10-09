package tools

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSocketDayGrantOfferIdentity(t *testing.T) {
	action := hitl.ProposedAction{Tool: "command", SessionID: "sess-sock"}
	grant := confine.SocketGrant{ApprovedPath: "/tmp/svc.sock", ResolvedPath: "/private/tmp/svc.sock"}
	offers := capabilitygrants.SocketExecutionGrantOffers(action, []confine.SocketGrant{grant})
	day, task := offers[0], offers[1]
	if day.ID == task.ID {
		t.Fatal("day and task socket offers must never share an id")
	}
	if !strings.HasPrefix(day.ID, "grant_") || !strings.HasPrefix(task.ID, "grant_") {
		t.Fatalf("socket lease ids must carry the uniform grant_ prefix: %q %q", day.ID, task.ID)
	}
	if day.TTLSeconds != hitl.DayRungTTLSeconds || day.Grant.TTLSeconds != hitl.DayRungTTLSeconds {
		t.Fatalf("day rung ttl = %d/%d want %d", day.TTLSeconds, day.Grant.TTLSeconds, hitl.DayRungTTLSeconds)
	}
	if day.Rung != hitl.ApprovalRungDay || task.Rung != hitl.ApprovalRungChat {
		t.Fatal("task rung must be the recommended face on this axis")
	}
	if day.Scope != hitl.ApprovalGrantScopeChat {
		t.Fatalf("day rung scope = %s want task (axis cap)", day.Scope)
	}
	if day.ExpiresWhen != hitl.ExpiresIn1DayOrChatDeleted {
		t.Fatalf("task-capped day rung expiry copy = %q", day.ExpiresWhen)
	}
	for _, offer := range []hitl.ApprovalGrantOffer{day, task} {
		if len(offer.Authority) != 1 || offer.Authority[0].Kind != hitl.AuthoritySocketChat {
			t.Fatalf("%s authority = %+v, want typed socket task authority", offer.Rung, offer.Authority)
		}
		if offer.Authority[0].Grant == nil || offer.Authority[0].Grant.ID != offer.ID {
			t.Fatalf("%s authority grant does not match offer: %+v", offer.Rung, offer.Authority[0].Grant)
		}
	}
	if !offers[2].Disabled {
		t.Fatal("project socket offer must be disabled without project identity")
	}
}

func TestSocketDayRungRidesProjectWhileDurableRungStaysTask(t *testing.T) {
	action := hitl.ProposedAction{Tool: "command", SessionID: "sess-sock", ProjectID: "proj-sock", ProjectDir: "/tmp/proj"}
	grant := confine.SocketGrant{ApprovedPath: "/tmp/svc.sock", ResolvedPath: "/private/tmp/svc.sock"}
	offers := capabilitygrants.SocketExecutionGrantOffers(action, []confine.SocketGrant{grant})
	if len(offers) != 3 {
		t.Fatalf("socket offers = %d, want day, task, and project: %+v", len(offers), offers)
	}
	day, task := offers[0], offers[1]
	if day.Scope != hitl.ApprovalGrantScopeProject || day.TTLSeconds != hitl.DayRungTTLSeconds {
		t.Fatalf("day rung = scope %s ttl %d, want project scope", day.Scope, day.TTLSeconds)
	}
	if day.Grant.ChatSessionID != "" {
		t.Fatalf("project day rung still bound to a task: %q", day.Grant.ChatSessionID)
	}
	if task.Scope != hitl.ApprovalGrantScopeChat || task.TTLSeconds != 0 {
		t.Fatalf("durable rung = scope %s ttl %d, want an unbounded task lease", task.Scope, task.TTLSeconds)
	}
	if day.ID == task.ID {
		t.Fatal("day and task socket offers must never share an id")
	}
	if day.Grant.ProjectID != "proj-sock" {
		t.Fatalf("project day grant project_id = %q", day.Grant.ProjectID)
	}
	// The day lease preserves the reviewed subject.
	if day.Grant.Predicate.Pattern != task.Grant.Predicate.Pattern {
		t.Fatalf("day rung changed subject: %q vs %q",
			day.Grant.Predicate.Pattern, task.Grant.Predicate.Pattern)
	}
	if day.Grant.Witness != task.Grant.Witness {
		t.Fatal("day rung dropped the boundary witness the task rung was reviewed under")
	}
	if len(day.Authority) != 1 || day.Authority[0].Kind != hitl.AuthorityGenericGrant {
		t.Fatalf("project day authority = %+v, want durable socket grant", day.Authority)
	}
	durable := day.Authority[0].Grant
	if durable == nil || durable.Predicate.Category != hitl.ApprovalGrantCategorySocketPath ||
		durable.ApprovedPath != grant.ApprovedPath || durable.ResolvedPath != grant.ResolvedPath {
		t.Fatalf("project day durable grant = %+v", durable)
	}
	if durable.ProjectID != action.ProjectID || durable.ProjectDir != action.ProjectDir ||
		day.Authority[0].TTLSeconds != hitl.DayRungTTLSeconds {
		t.Fatalf("project day durable scope = %+v", day.Authority[0])
	}
	if len(task.Authority) != 1 || task.Authority[0].Kind != hitl.AuthoritySocketChat {
		t.Fatalf("task authority = %+v, want task runtime", task.Authority)
	}
}

func TestSocketDayRungRidesIdentityWithoutFolder(t *testing.T) {
	action := hitl.ProposedAction{Tool: "command", SessionID: "sess-sock", ProjectID: "proj-sock"}
	grant := confine.SocketGrant{ApprovedPath: "/tmp/svc.sock", ResolvedPath: "/private/tmp/svc.sock"}
	offers := capabilitygrants.SocketExecutionGrantOffers(action, []confine.SocketGrant{grant})
	if offers[0].Scope != hitl.ApprovalGrantScopeProject || offers[0].Grant.ProjectID != "proj-sock" {
		t.Fatalf("no-folder socket day = %+v, want project identity", offers[0])
	}
}

func TestSocketDayRungStaysTaskWithoutProjectIdentity(t *testing.T) {
	action := hitl.ProposedAction{Tool: "command", SessionID: "sess-sock", ProjectDir: "/tmp/proj"}
	grant := confine.SocketGrant{ApprovedPath: "/tmp/svc.sock", ResolvedPath: "/private/tmp/svc.sock"}
	offers := capabilitygrants.SocketExecutionGrantOffers(action, []confine.SocketGrant{grant})
	if offers[0].Scope != hitl.ApprovalGrantScopeChat {
		t.Fatalf("folder-only socket day = %+v, want task", offers[0])
	}
}

type memorySocketRuntime struct {
	task    []confine.SocketGrant
	permits map[string]confine.SocketGrant
	used    map[string]bool
}

func (r *memorySocketRuntime) AppliedGrants(string) []confine.SocketGrant {
	return append([]confine.SocketGrant(nil), r.task...)
}

func (r *memorySocketRuntime) AuthorizedGrants(_, sessionID, toolCallID, actionDigest string, requested []confine.SocketGrant) []confine.SocketGrant {
	out := make([]confine.SocketGrant, 0, len(requested))
	for _, g := range requested {
		key := sessionID + "|" + toolCallID + "|" + actionDigest + "|" + g.ApprovedPath + "|" + g.ResolvedPath
		if r.used[key] {
			continue
		}
		if _, ok := r.permits[key]; ok {
			out = append(out, g)
		}
		for _, tg := range r.task {
			if tg.ApprovedPath == g.ApprovedPath && tg.ResolvedPath == g.ResolvedPath {
				out = append(out, g)
			}
		}
	}
	return out
}

func (r *memorySocketRuntime) GrantChat(_ string, g confine.SocketGrant, _, _, _ string, _ *time.Time) {
	r.task = append(r.task, g)
}

func (r *memorySocketRuntime) IssuePermit(sessionID, toolCallID, actionDigest string, g confine.SocketGrant) {
	if r.permits == nil {
		r.permits = map[string]confine.SocketGrant{}
	}
	key := sessionID + "|" + toolCallID + "|" + actionDigest + "|" + g.ApprovedPath + "|" + g.ResolvedPath
	r.permits[key] = g
}

func (r *memorySocketRuntime) ConsumePermit(sessionID, toolCallID, actionDigest string, g confine.SocketGrant) (bool, error) {
	key := sessionID + "|" + toolCallID + "|" + actionDigest + "|" + g.ApprovedPath + "|" + g.ResolvedPath
	if r.used == nil {
		r.used = map[string]bool{}
	}
	if r.used[key] {
		return false, nil
	}
	if _, ok := r.permits[key]; !ok {
		return false, nil
	}
	r.used[key] = true
	return true, nil
}

// Raw arguments cannot inject socket literals into the confine request without a host permit.
func TestConfineRequestForSpawnIgnoresForgedRawRequest(t *testing.T) {
	dir := shortTempDir(t)
	_ = listenUnixSocket(t, filepath.Join(dir, "s.sock"))

	tctx := ToolContext{
		SessionID:  "sess",
		ToolCallID: "call-1",
	}
	req, reject := ConfineRequestForSpawn(t.Context(), tctx, []string{dir})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if len(req.SocketGrants) != 0 {
		t.Fatalf("forged raw request must not supply SocketGrants, got %#v", req.SocketGrants)
	}
	contained := hitl.ContainedForRequest(req)
	if contained.SocketCount != 0 || contained.SocketPathsDigest != "" {
		t.Fatalf("contained must not carry socket facts from forged request: %+v", contained)
	}
}

func TestConfineRequestForSpawnConsumesPermit(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	_ = listenUnixSocket(t, sock)

	g, err := confine.ResolveSocketRequest(sock)
	testutil.FailErr(t, "ResolveSocketRequest", err)

	rt := &memorySocketRuntime{}
	digest := "action-digest"
	rt.IssuePermit("sess", "call-1", digest, g)

	tctx := ToolContext{
		SessionID:               "sess",
		ToolCallID:              "call-1",
		SocketGrants:            []confine.SocketGrant{g},
		SocketActionDigest:      digest,
		SocketCapabilityRuntime: rt,
	}
	req, reject := ConfineRequestForSpawn(t.Context(), tctx, []string{dir})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if len(req.SocketGrants) != 1 || req.SocketGrants[0].ResolvedPath != g.ResolvedPath {
		t.Fatalf("SocketGrants = %#v, want %#v", req.SocketGrants, g)
	}
	_, reject = ConfineRequestForSpawn(t.Context(), tctx, []string{dir})
	if reject == nil {
		t.Fatal("expected reject after permit consumption")
	}
	if reject.Code != "SANDBOX_SOCKET_PATH_CHANGED" {
		t.Fatalf("reject code = %q, want SANDBOX_SOCKET_PATH_CHANGED", reject.Code)
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lyc-sk-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "mkdir temp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func listenUnixSocket(t *testing.T, path string) net.Listener {
	t.Helper()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(path)
	})
	return ln
}
