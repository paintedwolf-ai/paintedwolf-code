package events

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func cf05F06RNG(idx int) *rand.Rand {
	sum := sha256.Sum256([]byte(fmt.Sprintf("20261127:F06:%d", idx)))
	return rand.New(rand.NewPCG(binary.BigEndian.Uint64(sum[0:8]), binary.BigEndian.Uint64(sum[8:16])))
}

// TestCF05_F06_SubscribersAndContinuityGapsInProcess runs 400 deterministically seeded F06 instances
// (seed 20261127) covering 1–6 clients, 1–20 drops/reconnects, hidden surfaces (0 subscriptions/wakes),
// valid cursor replay, and replay-window eviction -> ErrReplayUnavailable.
func TestCF05_F06_SubscribersAndContinuityGapsInProcess(t *testing.T) {
	viewer := testutil.HostOwner()
	const instances = 400

	for i := 0; i < instances; i++ {
		rng := cf05F06RNG(i)
		clientCount := 1 + rng.IntN(6)
		dropCount := 1 + rng.IntN(20)
		if i == 0 {
			clientCount, dropCount = 1, 1
		} else if i == instances-1 {
			clientCount, dropCount = 6, 20
		}

		hub := NewMemoryHub()
		projID := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)

		type clientState struct {
			ch     <-chan api.EventEnvelope
			unsub  func()
			cursor string
			wakes  int
		}
		clients := make([]*clientState, clientCount)
		for c := 0; c < clientCount; c++ {
			ch, unsub, err := hub.Subscribe(context.Background(), Subscription{Project: projID, Viewer: viewer})
			if err != nil {
				t.Fatalf("instance %d subscribe %d: %v", i, c, err)
			}
			clients[c] = &clientState{ch: ch, unsub: unsub}
		}

		hub.mu.Lock()
		activeSubs := len(hub.subscribers)
		hub.mu.Unlock()
		if activeSubs != clientCount {
			t.Fatalf("instance %d active subscribers=%d want %d", i, activeSubs, clientCount)
		}

		// Publish an immediate-topic burst (DebounceImmediate) and verify exact wake count per client.
		burstLen := 1 + rng.IntN(4)
		for b := 0; b < burstLen; b++ {
			err := hub.Publish(context.Background(), api.EventTopicActivity, PublishKey{Project: projID}, map[string]any{"seq": b})
			if err != nil {
				t.Fatalf("instance %d publish: %v", i, err)
			}
		}
		for c, cs := range clients {
			for b := 0; b < burstLen; b++ {
				select {
				case env := <-cs.ch:
					cs.cursor = env.Cursor
					cs.wakes++
				default:
					t.Fatalf("instance %d client %d missing event %d of %d", i, c, b, burstLen)
				}
			}
			if cs.wakes != burstLen {
				t.Fatalf("instance %d client %d wakes=%d want %d", i, c, cs.wakes, burstLen)
			}
		}

		// Drop and reconnect clients mid-burst using saved cursor.
		for d := 0; d < dropCount; d++ {
			victim := clients[d%clientCount]
			savedCursor := victim.cursor
			victim.unsub()

			// Hidden surface must NOT hold a subscription.
			hub.mu.Lock()
			gotSubs := len(hub.subscribers)
			hub.mu.Unlock()
			if gotSubs != clientCount-1 {
				t.Fatalf("instance %d drop %d activeSubs=%d want %d", i, d, gotSubs, clientCount-1)
			}

			// Emit one event while victim is disconnected.
			if err := hub.Publish(context.Background(), api.EventTopicActivity, PublishKey{Project: projID}, map[string]any{"drop": d}); err != nil {
				t.Fatalf("instance %d publish during drop %d: %v", i, d, err)
			}
			// Drain survivors.
			for idx, cs := range clients {
				if idx == d%clientCount {
					continue
				}
				select {
				case env := <-cs.ch:
					cs.cursor = env.Cursor
					cs.wakes++
				default:
					t.Fatalf("instance %d survivor %d missed event during drop %d", i, idx, d)
				}
			}

			// Reconnect victim with savedCursor -> must replay exactly the 1 missed event.
			ch, unsub, err := hub.Subscribe(context.Background(), Subscription{Project: projID, Viewer: viewer, After: savedCursor})
			if err != nil {
				t.Fatalf("instance %d reconnect drop %d failed: %v", i, d, err)
			}
			victim.ch = ch
			victim.unsub = unsub
			select {
			case env := <-victim.ch:
				victim.cursor = env.Cursor
				victim.wakes++
			default:
				t.Fatalf("instance %d reconnected client missed replayed event on drop %d", i, d)
			}
		}

		// Verify stale/invalid cursor triggers ErrReplayUnavailable.
		if _, _, err := hub.Subscribe(context.Background(), Subscription{Project: projID, Viewer: viewer, After: "bad.cursor"}); !errors.Is(err, ErrReplayUnavailable) {
			t.Fatalf("instance %d invalid cursor err=%v want ErrReplayUnavailable", i, err)
		}

		// Teardown all clients -> 0 active subscriptions.
		for _, cs := range clients {
			cs.unsub()
		}
		hub.mu.Lock()
		remaining := len(hub.subscribers)
		hub.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("instance %d leaked %d subscriptions after close", i, remaining)
		}
	}

	// Also verify ReplayCapacity eviction triggers ErrReplayUnavailable.
	hub := NewMemoryHub()
	projID := "00000000-0000-4000-8000-000000009999"
	ch, unsub, err := hub.Subscribe(context.Background(), Subscription{Project: projID, Viewer: viewer})
	if err != nil {
		t.Fatalf("eviction subscribe: %v", err)
	}
	if err := hub.Publish(context.Background(), api.EventTopicActivity, PublishKey{Project: projID}, map[string]any{"init": true}); err != nil {
		t.Fatalf("eviction init publish: %v", err)
	}
	oldCursor := (<-ch).Cursor
	unsub()
	for k := 0; k < ReplayCapacity+10; k++ {
		if err := hub.Publish(context.Background(), api.EventTopicActivity, PublishKey{Project: projID}, map[string]any{"k": k}); err != nil {
			t.Fatalf("eviction fill %d: %v", k, err)
		}
	}
	if _, _, err := hub.Subscribe(context.Background(), Subscription{Project: projID, Viewer: viewer, After: oldCursor}); !errors.Is(err, ErrReplayUnavailable) {
		t.Fatalf("evicted cursor err=%v want ErrReplayUnavailable", err)
	}
}
