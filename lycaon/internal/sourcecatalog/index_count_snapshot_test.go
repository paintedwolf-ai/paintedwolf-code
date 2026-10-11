package sourcecatalog

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFileCountReturnsUnknownWhileDiscoveryContinues(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "file.txt", "source")
	release, err := c.Trees.broker.Acquire(t.Context(), backgroundwork.Request{
		Key: "held", Lane: root.Path, Priority: backgroundwork.PriorityInteractive,
		Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
	})
	testutil.FailErr(t, "hold index preparation", err)
	defer release()
	ctx := testutil.BoundedContext(t, time.Second)
	scope := FileScope{Audience: AgentAudience, IncludeHidden: true}
	files, err := c.Trees.RootFileCount(ctx, "p", root, scope, 0)
	testutil.FailErr(t, "nonblocking cold count", err)
	if files.Measured {
		t.Fatalf("blocked discovery invented a measurement: %+v", files)
	}
	release()
	testutil.WaitFor(t, 5*time.Second, func() bool {
		files, err = c.Trees.RootFileCount(t.Context(), "p", root, scope, 0)
		return err == nil && files.Measured
	})
	if files.Count != 1 {
		t.Fatalf("background discovery lost the file: %+v", files)
	}
}
