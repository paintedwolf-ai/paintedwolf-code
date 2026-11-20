package privateguesthost

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/nixgeneration"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"path/filepath"
	"testing"
)

func TestPrivateServiceBundle(t *testing.T) {
	t.Setenv("LYCAON_ENGINE_ROOT", t.TempDir())
	for _, c := range []struct {
		host   string
		layout *nixgeneration.Layout
		dev    bool
		want   string
	}{
		{"/opt/painted-wolf-code/bin/pw", nil, true, "/opt/painted-wolf-code/engine-root/service-guest"},
		{"/nix/store/fixed/lib/painted-wolf-code/bin/pw", &nixgeneration.Layout{Root: "/nix/store/fixed/lib/painted-wolf-code"}, false, "/nix/store/fixed/lib/painted-wolf-code/engine-root/service-guest"},
		{"/test/pw", nil, true, "/test/service-guest"},
		{"/test/pw", nil, false, ""},
	} {
		got, err := serviceBundle(c.host, c.layout, c.dev)
		if got != c.want || errors.Is(err, serviceguest.ErrUnavailable) != (c.want == "") {
			t.Errorf("%s: %s, %v", c.host, got, err)
		}
	}
	dir := t.TempDir()
	if _, err := launchServiceBundle(t.Context(), filepath.Join(dir, "missing")); !errors.Is(err, serviceguest.ErrUnavailable) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := launchServiceBundle(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
