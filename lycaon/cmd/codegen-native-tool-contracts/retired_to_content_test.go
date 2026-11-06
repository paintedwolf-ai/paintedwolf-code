package main

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderedContractsListRetiredToContentToolsInOrder(t *testing.T) {
	out, err := renderToolContracts(nativemanifest.Config{RetiredToContentContract: map[string]string{
		"zeta_tool": "fixture.zeta", "alpha_tool": "fixture.alpha",
	}})
	testutil.FailErr(t, "render tool contracts", err)
	text := string(out)
	alpha, zeta := strings.Index(text, `"alpha_tool": "fixture.alpha"`), strings.Index(text, `"zeta_tool": "fixture.zeta"`)
	if alpha < 0 || zeta < alpha {
		t.Fatalf("retired entries missing or out of order:\n%s", text)
	}
}
