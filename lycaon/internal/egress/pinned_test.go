package egress_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/lycaon/lycaon/internal/egress"
)

func TestPinnedTransportRejectsAnotherDialHost(t *testing.T) {
	tr := egress.PinnedTransport("feed.example", []netip.Addr{netip.MustParseAddr("127.0.0.1")}, 0)
	defer tr.CloseIdleConnections()
	if tr.Proxy != nil {
		t.Fatal("pinned transport enables proxy discovery")
	}
	conn, err := tr.DialContext(t.Context(), "tcp", "other.example:443")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("dialed an unvalidated host")
	}
	var denied *egress.DestinationDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("unexpected dial result: %v", err)
	}
}
