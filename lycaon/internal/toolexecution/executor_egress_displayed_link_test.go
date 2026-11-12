package toolexecution_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/testutil"
)

// contactLog records the contacts the executor writes for allowed dials.
type contactLog struct {
	mu    sync.Mutex
	kinds []evidence.Contact
}

func (*contactLog) SessionContacts(string) map[string]evidence.Contact { return nil }

func (l *contactLog) RecordContact(_ context.Context, _, _ string, contact evidence.Contact) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.kinds = append(l.kinds, contact)
	return nil
}

// A reader following a displayed link is a quiet retrieval while the chat is
// known to hold no credential. Once it holds one, or exposure is unknown, the
// dial is reviewed and recorded as the chat's own. A source stays a retrieval
// either way.
func TestDisplayedLinkBecomesTheChatsOwnDialOnceACredentialIsHeld(t *testing.T) {
	exposed := func(held bool) func(context.Context, string) (bool, error) {
		return func(context.Context, string) (bool, error) { return held, nil }
	}
	for name, tc := range map[string]struct {
		origin   confine.AddressOrigin
		exposure func(context.Context, string) (bool, error)
		asks     int
		contact  evidence.Contact
	}{
		"displayed link, no credential":     {origin: confine.AddressDisplayed, exposure: exposed(false), contact: evidence.ContactRetrieval},
		"displayed link, credential held":   {origin: confine.AddressDisplayed, exposure: exposed(true), asks: 1, contact: evidence.ContactOwn},
		"displayed link, exposure unknown":  {origin: confine.AddressDisplayed, asks: 1, contact: evidence.ContactOwn},
		"source, credential held":           {origin: confine.AddressSource, exposure: exposed(true), contact: evidence.ContactRetrieval},
		"authored address, no credential":   {origin: confine.AddressAuthored, exposure: exposed(false), asks: 1, contact: evidence.ContactOwn},
		"authored address, credential held": {origin: confine.AddressAuthored, exposure: exposed(true), asks: 1, contact: evidence.ContactOwn},
	} {
		t.Run(name, func(t *testing.T) {
			exec, mgr := gatherExecutor(t)
			contacts := &contactLog{}
			exec.Network.SetSessionHostLedger(contacts)
			if tc.exposure != nil {
				exec.Secrets.SetSecretExposureSource(tc.exposure)
			}
			ctx, cancel := context.WithTimeout(context.Background(), testutil.Timeout(time.Minute))
			defer cancel()
			cmd := confine.EgressCommand{
				SessionID: "chat", RootSessionID: "chat", ToolCallID: "call", ProjectID: "proj", ProjectDir: "/tmp/proj",
				Image: ingestion.ToolFetchURL, Origin: tc.origin,
			}
			if !exec.ResolveEgressForTest(ctx, cmd, "page.example") {
				t.Fatal("the dial was refused")
			}
			if got := len(mgr.requests()); got != tc.asks {
				t.Fatalf("asks = %d, want %d", got, tc.asks)
			}
			if !slices.Equal(contacts.kinds, []evidence.Contact{tc.contact}) {
				t.Fatalf("recorded contacts = %v, want %v", contacts.kinds, tc.contact)
			}
		})
	}
}
