package secretmatch

import (
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecipientIdentityBindsOriginAndSurface(t *testing.T) {
	origin := func(address string) Recipient {
		target, err := url.Parse(address)
		testutil.FailErr(t, "parse origin", err)
		id, label := HTTPDestination(target)
		return Recipient{ID: id, Label: label, Surface: SurfaceHTTPRequest, Kind: DestinationService}
	}
	receiver := origin("https://LOCALHOST/login")
	same := origin("https://localhost:443/admin?x=1")
	if receiver.ID != same.ID {
		t.Fatal("paths or explicit default port changed service identity")
	}
	for _, other := range []Recipient{origin("http://localhost:443"), origin("https://localhost:444"), origin("https://other.test")} {
		if RecipientCovered([]Recipient{receiver}, other.ID, string(other.Surface)) {
			t.Fatal("permission crossed an origin boundary")
		}
	}
	if RecipientCovered([]Recipient{receiver}, receiver.ID, string(SurfaceMCP)) {
		t.Fatal("permission crossed a surface boundary")
	}
	ipv6 := origin("http://[::1]:8080")
	if ipv6.Label != "http://[::1]:8080" {
		t.Fatalf("IPv6 origin = %q", ipv6.Label)
	}
}

func TestRecipientSetCanonicalization(t *testing.T) {
	a := Recipient{ID: "a", Label: "A", Surface: SurfaceMCP, Kind: DestinationService}
	b := Recipient{ID: "b", Label: "B", Surface: SurfaceCommand, Kind: DestinationProcess}
	set, err := CanonicalRecipients([]Recipient{a, b, a})
	testutil.FailErr(t, "canonicalize", err)
	if len(set) != 2 || RecipientDigest(set) != RecipientDigest([]Recipient{b, a}) {
		t.Fatal("recipient set is order dependent")
	}
	for _, bad := range [][]Recipient{nil, {{ID: "a", Label: "A", Surface: SurfaceMCP, Kind: DestinationProcess}}, {a, {ID: "a", Label: "Other", Surface: SurfaceMCP, Kind: DestinationService}}} {
		if _, err := CanonicalRecipients(bad); err == nil {
			t.Fatal("invalid recipient authority accepted")
		}
	}
}
