package privateguesthost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/nixgeneration"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"github.com/lycaon/lycaon/internal/testutil"
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
		t.Fatalf("missing service bundle: got %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := launchServiceBundle(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled service launch: got %v", err)
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--acquire-native" {
		os.Exit(handshakeFixtureStatus(os.Stdin))
	}
	os.Exit(m.Run())
}

func TestPrivateServicePackageActivation(t *testing.T) {
	host, err := os.Executable()
	testutil.FailErr(t, "locate test executable", err)
	bundle := filepath.Join(filepath.Dir(host), "service-guest")
	testutil.FailErr(t, "reserve fixture bundle", os.Mkdir(bundle, 0700))
	t.Cleanup(func() { _ = os.RemoveAll(bundle) })
	root := filepath.Join(bundle, "rootfs")
	testutil.FailErr(t, "stage root filesystem", os.Mkdir(root, 0700))
	testutil.FailErr(t, "stage protocol fixture", os.Symlink(host, filepath.Join(root, "init")))
	startup, cancel := context.WithCancel(t.Context())
	defer cancel()
	client, err := OpenPrivateGuest(startup)
	testutil.FailErr(t, "open packaged fixture", err)
	cancel()
	_, err = client.Run(t.Context(), []string{"fixture"})
	testutil.FailErr(t, "run after releasing startup context", err)
	testutil.FailErr(t, "release and reap packaged fixture", client.Close())
}
