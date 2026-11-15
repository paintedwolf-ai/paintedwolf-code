package adapter_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

func TestRecordedConnectionsRoundTripAndRefuseUnknownShapes(t *testing.T) {
	dir := t.TempDir()
	if got, err := adapter.LoadRecordedConnections(dir); err != nil || got != nil {
		t.Fatalf("empty view: %+v err=%v", got, err)
	}
	rec := adapter.RecordedConnection{
		AdapterID: adapter.AdapterFUSELinux, ViewID: "view", RootID: "primary", MountPath: "/mnt/view",
		FSName: "lycaon-view-primary", DevMajorMinor: "0:51", Server: adapter.ProcessIdentity{PID: 7, Started: 9},
	}
	if err := adapter.WriteRecordedConnection(dir, rec); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := adapter.LoadRecordedConnections(dir)
	if err != nil || len(got) != 1 || got[0].DevMajorMinor != "0:51" || got[0].Server.Started != 9 || got[0].FormatVersion != adapter.RecordedConnectionFormat {
		t.Fatalf("loaded %+v err=%v", got, err)
	}
	path := filepath.Join(dir, "connections", "primary.json")
	for _, unknown := range []string{
		`{"format_version":99,"view_id":"view","root_id":"primary","mount_path":"/m"}`,
		`{"format_version":1,"view_id":"view","root_id":"primary","mount_path":"/m","future":true}`,
		`{"format_version":1,"view_id":"view","root_id":"other","mount_path":"/m"}`,
		`not json`,
	} {
		if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := adapter.LoadRecordedConnections(dir); !errors.Is(err, adapter.ErrInvalidConnectionRecord) {
			t.Fatalf("%s: err=%v", unknown, err)
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != unknown {
			t.Fatalf("refused record must stay untouched: %q err=%v", raw, err)
		}
	}
}

func TestProcessIdentityEndsOnPIDReuseOrEarlierBoot(t *testing.T) {
	self, err := adapter.CurrentProcessIdentity(os.Getpid())
	if err != nil {
		t.Skipf("no process identity on this platform: %v", err)
	}
	if self.Ended() {
		t.Fatal("this process reads as ended")
	}
	if !(adapter.ProcessIdentity{PID: self.PID, Started: self.Started + 1, BootID: self.BootID}).Ended() {
		t.Fatal("reused pid must read as ended")
	}
	if self.BootID != "" && !(adapter.ProcessIdentity{PID: self.PID, Started: self.Started, BootID: "another-boot"}).Ended() {
		t.Fatal("an identity from another boot must read as ended")
	}
}
