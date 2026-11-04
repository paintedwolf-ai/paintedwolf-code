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
// the session through a freshly observed inherited descriptor. A process another
// user keeps unreadable is never proof of absence: its holders stay unknown until
// the platform shows the kernel excludes them from this endpoint. Without that
// proof callers retain ordinary approval rather than substituting a requested
// port or a partial native/process listing.
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
	walk, err := walkEndpointHolders(ctx, endpoint, owned, pids, SocketsOf, unreadableByThisUser)
	if err != nil {
		return "", false, err
	}
	if walk.foreign || len(walk.holders) == 0 {
		return "", false, nil
	}
	// No closed/replaced descriptor or departed lineage can survive the join.
	current, err := sessionSockets(ctx, session)
	if err != nil {
		return "", false, err
	}
	var holders []string
	for _, holder := range walk.holders {
		key := socketIdentity(holder)
		if !current[key] {
			return "", false, nil
		}
		holders = append(holders, key)
	}
	if walk.unread && !unreadHoldersExcluded(endpoint, walk) {
		return "", false, ErrObservationIncomplete
	}
	sort.Strings(holders)
	identity := sha256.Sum256([]byte(endpoint.String() + "\x00" + strings.Join(holders, "\x00")))
	return fmt.Sprintf("session-listener:%x", identity), true, nil
}

// endpointHolders is one walk of the process table for a loopback endpoint.
type endpointHolders struct {
	// holders are the session's listeners bound to exactly this endpoint.
	holders []SocketObservation
	// sibling holds the session's listeners on the other loopback family's
	// address and this port, which a port-scoped grant also reaches.
	sibling []SocketObservation
	// foreign means a readable holder outside the session, or any wildcard holder.
	foreign bool
	// unread means another user's process was skipped, so its holders are unknown.
	unread bool
}

// walkEndpointHolders stops at the first foreign holder. Only a process denied
// as another user's is skipped; any other read failure leaves the walk incomplete.
func walkEndpointHolders(
	ctx context.Context,
	endpoint netip.AddrPort,
	owned map[string]bool,
	pids []int,
	observe func(pid int) ([]SocketObservation, error),
	denied func(pid int, err error) bool,
) (endpointHolders, error) {
	var walk endpointHolders
	target := endpoint.Addr().Unmap()
	sibling := siblingLoopback(target)
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return endpointHolders{}, err
		}
		sockets, err := observe(pid)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			if denied(pid, err) {
				walk.unread = true
				continue
			}
			return endpointHolders{}, fmt.Errorf("%w: %w", ErrObservationIncomplete, err)
		}
		for _, socket := range sockets {
			if !socket.Listening || socket.Local.Port() != endpoint.Port() {
				continue
			}
			addr := socket.Local.Addr().Unmap()
			isOwned := owned[socketIdentity(socket)]
			switch {
			case addr == sibling:
				if isOwned {
					walk.sibling = append(walk.sibling, socket)
				}
			// Wildcard listeners are externally exposed; ownership does not silence
			// their connection admission. Consumer policy can still approve explicitly.
			case addr.IsUnspecified() || (addr == target && !isOwned):
				walk.foreign = true
				return walk, nil
			case addr == target:
				walk.holders = append(walk.holders, socket)
			}
		}
	}
	return walk, nil
}

func siblingLoopback(addr netip.Addr) netip.Addr {
	if addr.Unmap().Is4() {
		return netip.IPv6Loopback()
	}
	return netip.AddrFrom4([4]byte{127, 0, 0, 1})
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
