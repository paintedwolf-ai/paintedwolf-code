package tools

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

type failingPermitRuntime struct{ memorySocketRuntime }

func (*failingPermitRuntime) ConsumePermit(hitl.EffectPermitIdentity, string, confine.SocketGrant) (bool, error) {
	return false, errors.New("permit store unavailable")
}

func TestSocketPermitRecoveryDoesNotClaimBindingChanged(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "service.sock")
	_ = listenUnixSocket(t, path)
	grant, err := confine.ResolveSocketRequest(path)
	testutil.FailErr(t, "resolve socket", err)
	ctx, scope := socketref.WithScope(t.Context())
	defer scope.Close()
	grant, err = confine.CaptureSocketGrant(ctx, grant)
	testutil.FailErr(t, "capture socket", err)
	for _, runtime := range []SocketCapabilityRuntime{nil, &memorySocketRuntime{}, &failingPermitRuntime{}} {
		tc := ToolContext{Socket: InvocationSocket{SocketGrants: []confine.SocketGrant{grant}, SocketCapabilityRuntime: runtime}}
		_, reject := FinalizeSocketGrantsForSpawn(ctx, tc)
		if reject == nil || reject.Code != isolation.CodeSocketPermitRequired {
			t.Fatalf("permit rejection = %+v", reject)
		}
	}
	_, reject := ClaimDeclaredSocket(ctx, ToolContext{}, path)
	if reject == nil || reject.Code != isolation.CodeSocketPermitRequired {
		t.Fatalf("undeclared socket rejection = %+v", reject)
	}
}
