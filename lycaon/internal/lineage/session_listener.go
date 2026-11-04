package lineage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// ErrObservationIncomplete means the kernel did not expose enough holder facts
// to prove exclusive session ownership. It never denotes an unowned empty set.
var ErrObservationIncomplete = errors.New("native socket holder observation is incomplete")

// ObserveSessionListener requires every relevant current socket and holder to
// belong to the session through a freshly observed inherited descriptor. The
// host-wide table supplies the sockets, so a foreign listener is seen whether or
// not its process can be inspected, and a socket with no readable holder is never
// proof. A process this user cannot read could share an owned socket only if a
// session member passed it on. A table the kernel narrowed proves nothing;
// callers must then retain ordinary approval rather than substituting a requested
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
	table, err := CompleteListeners(ListenerQuery{Protocols: []Protocol{ProtocolTCP}, Port: endpoint.Port()})
	if err != nil {
		if errors.Is(err, ErrObservationIncomplete) {
			return "", false, err
		}
		return "", false, fmt.Errorf("%w: %w", ErrObservationIncomplete, err)
	}
	holders, exclusive := exclusiveHolders(table, endpoint, owned)
	if !exclusive {
		return "", false, nil
	}
	// No closed/replaced descriptor or departed lineage can survive the join.
	current, err := sessionSockets(ctx, session)
	if err != nil {
		return "", false, err
	}
	var identities []string
	for _, holder := range holders {
		identity, ok := current[holder]
		if !ok {
			return "", false, nil
		}
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	identity := sha256.Sum256([]byte(endpoint.String() + "\x00" + strings.Join(identities, "\x00")))
	return fmt.Sprintf("session-listener:%x", identity), true, nil
}

// ownedHolder is one session member holding one listening address.
type ownedHolder struct {
	Holder ProcessIdentity
	Local  netip.AddrPort
}

func newOwnedHolder(holder ProcessIdentity, local netip.AddrPort) ownedHolder {
	return ownedHolder{Holder: holder, Local: netip.AddrPortFrom(local.Addr().Unmap(), local.Port())}
}

// exclusiveHolders returns the session members serving endpoint, or false when
// any socket that answers there has a holder outside the session or none readable.
// Wildcard listeners are externally exposed; ownership does not silence their
// connection admission. Consumer policy can still approve explicitly.
func exclusiveHolders(table []Listener, endpoint netip.AddrPort, owned map[ownedHolder]string) ([]ownedHolder, bool) {
	var holders []ownedHolder
	for _, listener := range table {
		addr := listener.Addr.Addr().Unmap()
		if listener.Addr.Port() != endpoint.Port() || (addr != endpoint.Addr().Unmap() && !addr.IsUnspecified()) {
			continue
		}
		if addr.IsUnspecified() || len(listener.Holders) == 0 {
			return nil, false
		}
		for _, holder := range listener.Holders {
			key := newOwnedHolder(holder, listener.Addr)
			if _, ok := owned[key]; !ok {
				return nil, false
			}
			holders = append(holders, key)
		}
	}
	return holders, len(holders) > 0
}

func socketIdentity(socket SocketObservation) string {
	return fmt.Sprintf("%s/%d/%d/%s", socket.ResourceID, socket.Holder.PID, socket.Holder.Started, socket.Local)
}

func sessionSockets(ctx context.Context, session string) (map[ownedHolder]string, error) {
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
	owned := map[ownedHolder]string{}
	for _, id := range ids {
		sockets, err := OwnedSockets(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, socket := range sockets {
			if socket.Listening {
				owned[newOwnedHolder(socket.Holder, socket.Local)] = socketIdentity(socket)
			}
		}
	}
	return owned, nil
}
