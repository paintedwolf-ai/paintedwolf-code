//go:build linux || darwin

package api

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHarnessSocketCardUsesRealReferenceOrExplicitPathnameMode(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "hs-") //nolint:usetesting // Kernel Unix addresses are bounded.
	testutil.FailErr(t, "harness socket fixture", err)
	defer os.RemoveAll(root)
	path := filepath.Join(root, "service")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "harness socket listener", err)
	defer listener.Close()
	ctx, scope := socketref.WithScope(t.Context())
	defer scope.Close()
	grant, err := harnessSocketGrant(ctx, path, "")
	testutil.FailErr(t, "harness socket grant", err)
	request := hitl.CheckpointRequest{SessionID: "chat", ProposedAction: &hitl.ProposedAction{}}
	applyHarnessSocketCapability(&request, grant, "", "")
	if len(request.SocketCapability.Targets) != 1 || request.SocketCapability.Targets[0].Binding != grant.Binding {
		t.Fatal("harness lost realization identity")
	}
	if socketref.Platform().Command == socketref.RetainedObject {
		if grant.Binding.ID == "" || grant.Binding.Mode() != socketref.RetainedObject {
			t.Fatal("harness minted unbound object authority")
		}
		if _, err := harnessSocketGrant(ctx, path, path+"-different"); err == nil {
			t.Fatal("harness accepted a fictional resolved target")
		}
	} else if grant.Binding.Mode() != socketref.KernelPathname || grant.Binding.ID != "" {
		t.Fatal("pathname layout card minted native authority")
	}
	scope.Close()
	if socketref.Platform().Command == socketref.RetainedObject {
		owner, err := grant.Binding.Acquire()
		if !errors.Is(err, socketref.ErrUnbound) {
			if owner != nil {
				owner.Close()
			}
			t.Fatalf("harness scope retained authority without checkpoint transfer: %v", err)
		}
		if _, err := harnessSocketGrant(t.Context(), path, ""); err == nil {
			t.Fatal("harness captured without a lifecycle owner")
		}
	} else {
		synthetic, err := harnessSocketGrant(t.Context(), path+"-layout-only", "")
		testutil.FailErr(t, "explicit pathname layout card", err)
		if synthetic.Binding.Mode() != socketref.KernelPathname {
			t.Fatal("layout became native object authority")
		}
	}
}
