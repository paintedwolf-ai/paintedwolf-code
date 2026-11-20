package privateguesthost

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/serviceguest"
	"github.com/lycaon/lycaon/internal/testutil"
)

type handshakeFixture struct{ serviceguest.Executor }

func (handshakeFixture) Close() error { return nil }

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "pw-service-vm" {
		for i, name := range []string{"manifest.json", "kernel", "initramfs.gz"} {
			file := os.NewFile(uintptr(4+i), name)
			data, err := io.ReadAll(file)
			if err != nil || string(data) != name {
				os.Exit(2)
			}
			_ = file.Close()
		}
		channel := os.NewFile(3, "activation")
		if err := serviceguest.Serve(context.Background(), channel, handshakeFixture{}); err != nil && !errors.Is(err, io.EOF) {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPrivateGuestAuthenticatesRunningImage(t *testing.T) {
	host, err := os.Executable()
	testutil.FailErr(t, "locate fixture executable", err)
	bundle := filepath.Join(filepath.Dir(host), "service-guest")
	testutil.FailErr(t, "stage fixture runtime", os.Mkdir(bundle, 0700))
	t.Cleanup(func() { _ = os.RemoveAll(bundle) })
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	path := filepath.Join(bundle, "pw-service-vm")
	image, err := os.ReadFile(host)
	testutil.FailErr(t, "capture native test executable", err)
	testutil.FailErr(t, "write fixture helper", os.WriteFile(path, image, 0700))
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", path).CombinedOutput(); err != nil {
		t.Fatalf("sign fixture: %v: %s", err, out)
	}
	output, err := exec.Command("/usr/bin/codesign", "-d", "--verbose=4", path).CombinedOutput()
	testutil.FailErr(t, "read fixture signing identity", err)
	var cdhash string
	for line := range strings.Lines(string(output)) {
		if value, ok := strings.CutPrefix(line, "CDHash="); ok {
			cdhash = strings.TrimSpace(value)
			break
		}
	}
	if raw, err := hex.DecodeString(cdhash); err != nil || len(raw) != 20 {
		t.Fatalf("invalid fixture identity %q", cdhash)
	}
	for _, name := range []string{"manifest.json", "kernel", "initramfs.gz"} {
		testutil.FailErr(t, "stage boot resource", os.WriteFile(filepath.Join(bundle, name), []byte(name), 0600))
	}
	identity := filepath.Join(bundle, "pw-service-vm.cdhash")
	testutil.FailErr(t, "stage unrelated code identity", os.WriteFile(identity, []byte(strings.Repeat("0", 40)), 0600))
	if _, err := OpenPrivateGuest(t.Context()); !errors.Is(err, serviceguest.ErrUnavailable) {
		t.Fatalf("unexpected image result: %v", err)
	}
	testutil.FailErr(t, "stage the signed code identity", os.WriteFile(identity, []byte(cdhash), 0600))
	client, err := OpenPrivateGuest(t.Context())
	testutil.FailErr(t, "authenticate fixture", err)
	testutil.FailErr(t, "release fixture", client.Close())
}
