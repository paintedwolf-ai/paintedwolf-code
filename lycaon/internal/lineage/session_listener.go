package lineage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"syscall"
)

// ErrObservationIncomplete means the kernel did not expose enough holder facts
// to prove exclusive session ownership. It never denotes an unowned empty set.
var ErrObservationIncomplete = errors.New("native socket holder observation is incomplete")

// ObserveSessionListener requires every relevant current holder to belong to
// the session through a freshly observed inherited descriptor. Inaccessible
// processes prevent the all-holder proof; callers must retain ordinary approval
// rather than substituting a requested port or a partial native/process listing.
func ObserveSessionListener(ctx context.Context, session string, endpoint netip.AddrPort) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if !supported {
		return "", false, ErrUnsupported
	}
	if session == "" || !endpoint.IsValid() || !endpoint.Addr().IsLoopback() {
		return "", false, nil
	}
	owned, err := sessionSockets(ctx, session)
	if err != nil {
		return "", false, err
	}
	if len(owned) == 0 {
		return "", false, nil
	}
	pids, err := livePIDs()
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", ErrObservationIncomplete, err)
	}
	var holders []string
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		sockets, err := SocketsOf(pid)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("%w: %w", ErrObservationIncomplete, err)
		}
		for _, socket := range sockets {
			if !socket.Listening || socket.Local.Port() != endpoint.Port() {
				continue
			}
			addr := socket.Local.Addr().Unmap()
			if addr != endpoint.Addr().Unmap() && !addr.IsUnspecified() {
				continue
			}
			// Wildcard listeners are externally exposed; ownership does not silence
			// their connection admission. Consumer policy can still approve explicitly.
			key := socketIdentity(socket)
			if addr.IsUnspecified() || !owned[key] {
				return "", false, nil
			}
			holders = append(holders, key)
		}
	}
	if len(holders) == 0 {
		return "", false, nil
	}
	// No closed/replaced descriptor or departed lineage can survive the join.
	current, err := sessionSockets(ctx, session)
	if err != nil {
		return "", false, err
	}
	for _, holder := range holders {
		if !current[holder] {
			return "", false, nil
		}
	}
	sort.Strings(holders)
	identity := sha256.Sum256([]byte(endpoint.String() + "\x00" + strings.Join(holders, "\x00")))
	return fmt.Sprintf("session-listener:%x", identity), true, nil
}

func socketIdentity(socket SocketObservation) string {
	return fmt.Sprintf("%s/%d/%d/%s", socket.ResourceID, socket.Holder.PID, socket.Holder.Started, socket.Local)
}

func sessionSockets(ctx context.Context, session string) (map[string]bool, error) {
	registry.mu.Lock()
	var ids []ID
	for id, l := range registry.live {
		if l.session == session {
			ids = append(ids, id)
		}
	}
	for _, l := range registry.retired {
		if l.session == session {
			ids = append(ids, l.id)
		}
	}
	registry.mu.Unlock()
	owned := map[string]bool{}
	for _, id := range ids {
		sockets, err := OwnedSockets(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, socket := range sockets {
			if socket.Listening {
				owned[socketIdentity(socket)] = true
			}
		}
	}
	return owned, nil
}
