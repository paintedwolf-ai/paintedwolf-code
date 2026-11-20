package confinetest

import (
	"errors"
	"net"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func StartBroker(t testing.TB) {
	t.Helper()
	_, err := confine.StartEgressBroker(t.TempDir(), net.Listen)
	testutil.FailErr(t, "start egress broker", err)
	stopWithTest(t)
}

func FailBrokerStartup(t testing.TB, cause error) {
	t.Helper()
	_, err := confine.StartEgressBroker(t.TempDir(), func(string, string) (net.Listener, error) {
		return nil, cause
	})
	if !errors.Is(err, cause) {
		t.Fatalf("start egress broker = %v, want %v", err, cause)
	}
	stopWithTest(t)
}

func stopWithTest(t testing.TB) {
	t.Helper()
	t.Cleanup(func() {
		testutil.FailErr(t, "stop egress broker", confine.StopEgressBroker())
	})
}
