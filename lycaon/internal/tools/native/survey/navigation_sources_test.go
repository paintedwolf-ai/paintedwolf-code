package survey

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestFindSourceContextContainsOnlyDeliveredPage(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		testutil.FailErr(t, "write candidate", os.WriteFile(filepath.Join(root, fmt.Sprintf("file%d.go", i)), []byte("package main"), 0600))
	}
	tctx := nativefixture.Context(root)
	tctx.Identity.ProjectID = "p"
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	output, err := tool.Run(t.Context(), map[string]any{"name_glob": "*.go", "max_results": 2, "view": "raw"}, tctx)
	testutil.FailErr(t, "find page", err)
	response := parseFindResponse(t, output)
	if len(response.Results) != 2 || tctx.Effects.Out.SourceContext == nil || len(tctx.Effects.Out.SourceContext.Locations) != 2 {
		t.Fatalf("page=%+v context=%+v", response, tctx.Effects.Out.SourceContext)
	}
	for i, location := range tctx.Effects.Out.SourceContext.Locations {
		if location.Path != response.Results[i].Path || location.RootID != "r1" {
			t.Fatalf("source=%+v result=%+v", location, response.Results[i])
		}
	}
}
