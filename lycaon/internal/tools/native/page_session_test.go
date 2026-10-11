package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/page"
)

func TestPageOpenMissingTarget(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	pool := browser.NewPool("")
	defer pool.Close()
	pages := pagesession.NewRegistry(pagesession.DefaultConfig())
	defer pages.Close(t.Context())
	testutil.FailErr(t, "register", RegisterPageSessionTools(reg, pool, pages, nil, nil))
	_, err := reg.Run(context.Background(), page.OpenToolName, map[string]any{}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s"},
		Effects:  tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	})
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "CAPTURE_TARGET_INVALID" {
		t.Fatalf("err=%v", err)
	}
}
