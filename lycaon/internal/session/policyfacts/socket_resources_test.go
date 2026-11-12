package policyfacts

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
)

// A refused socket that is a host resource's local service names that resource.
func TestRefusedSocketsNameTheirHostResources(t *testing.T) {
	m := New(nil)
	var asked []string
	m.SetSocketResources(func(_ context.Context, projectID string, paths []string) []string {
		asked = append([]string{projectID}, paths...)
		return []string{"docker"}
	})
	gc := oar.NewGuardContext()
	m.observeSocketResources(t.Context(), gc, "project")
	if asked != nil || gc.Refusals.RefusedSocketHostResources != nil {
		t.Fatal("resources looked up without a refused socket")
	}
	gc.Refusals.RefusedSocketPaths = []string{"/run/engine.sock"}
	m.observeSocketResources(t.Context(), gc, "project")
	if !slices.Equal(asked, []string{"project", "/run/engine.sock"}) || !slices.Equal(gc.Refusals.RefusedSocketHostResources, []string{"docker"}) {
		t.Fatalf("asked %v, published %v", asked, gc.Refusals.RefusedSocketHostResources)
	}
}
