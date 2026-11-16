package survey

import (
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
)

// holdMetadataLane occupies the catalog's traversal lane for one root, so a
// build for it cannot be admitted until the returned release runs.
func holdMetadataLane(t *testing.T, root string) func() {
	t.Helper()
	release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{
		Key: "survey-inventory-test", Lane: root, Priority: backgroundwork.PriorityInteractive,
		Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
	})
	if err != nil {
		testutil.FailErr(t, "hold the metadata lane", err)
	}
	return release
}
