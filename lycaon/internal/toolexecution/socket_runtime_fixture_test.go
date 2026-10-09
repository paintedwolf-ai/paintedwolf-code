package toolexecution

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

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
