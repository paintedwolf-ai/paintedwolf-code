// Package lineage tracks live process descendants of confined actions via inherited descriptors.
package lineage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"time"
)

// ChildFD is the descriptor the launched process receives, clear of the ones
// shells assign themselves (3-9) and under the 256-descriptor floor a process
// can expect.
const ChildFD = 199

// ErrUnsupported reports a platform without descendant observation.
var ErrUnsupported = errors.New("process lineage observation is unavailable on this platform")

// memberTTL bounds reuse of a known member set. Attribution reads it far more
// often than descendants appear.
const memberTTL = 250 * time.Millisecond

// scanGate coalesces concurrent walks. An unknown process always forces a fresh
// walk, since each new process must be attributable on its first dial; callers
// arriving together share that walk.
var scanGate = make(chan struct{}, 1)

// retainedRetired bounds how many ended lineages stay resolvable. Past it, the
// oldest is released and its descendants become unattributable.
const retainedRetired = 256

// State is a lineage's relationship to a live owner.
type State string

const (
	// StateLive means an owner still holds this lineage.
	StateLive State = "live"
	// StateRetired means the owning action ended and nothing adopted it.
	StateRetired State = "retired"
)

// ID identifies one action's lineage.
type ID string

// Lineage is the host's handle on the descendants of one launch.
type Lineage struct {
	id ID
	// read is the engine's end; its peer handle identifies descendants.
	read *os.File
	// write is handed to the launched process and closed by the engine after start.
	write *os.File
	// peer is the kernel identity of the write end.
	peer uint64
	// owner names the subject currently accountable for these descendants.
	owner string
	// session is the task session whose action launched these descendants.
	session string

	mu       sync.Mutex
	members  []int
	scanned  time.Time
	state    State
	released bool
}

var registry = struct {
	mu       sync.Mutex
	live     map[ID]*Lineage
	retired  []*Lineage
	lastScan time.Time
}{live: map[ID]*Lineage{}}

// Open creates a lineage owned by subject on behalf of a task session and
// registers it as live.
func Open(subject, session string) (*Lineage, error) {
	if !supported {
		return nil, ErrUnsupported
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("lineage pipe: %w", err)
	}
	peer, err := pipePeerHandle(read)
	if err != nil {
		_ = read.Close()
		_ = write.Close()
		return nil, err
	}
	l := &Lineage{id: newID(), read: read, write: write, peer: peer, owner: subject, session: session, state: StateLive}
	registry.mu.Lock()
	registry.live[l.id] = l
	registry.mu.Unlock()
	return l, nil
}

// ID returns the lineage identity. It is never placed in a child environment.
func (l *Lineage) ID() ID {
	if l == nil {
		return ""
	}
	return l.id
}

// Owner names the subject currently accountable for these descendants.
func (l *Lineage) Owner() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.owner
}

// Session is the task session that launched these descendants; empty for a
// launch no session made.
func (l *Lineage) Session() string {
	if l == nil {
		return ""
	}
	return l.session
}

// State reports whether an owner still holds this lineage.
func (l *Lineage) State() State {
	if l == nil {
		return StateRetired
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// ChildFile is the descriptor to hand the launched process.
func (l *Lineage) ChildFile() *os.File {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.write
}

// Started releases the engine's write end so the pipe reports end-of-file once
// the last descendant exits. It is called after the process starts.
func (l *Lineage) Started() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeWriteLocked()
}

func (l *Lineage) closeWriteLocked() {
	if l.write != nil {
		_ = l.write.Close()
		l.write = nil
	}
}

// Live reports whether any descendant still holds the descriptor.
func (l *Lineage) Live() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.read == nil {
		return false
	}
	if l.write != nil {
		// The engine still holds a write end, so the pipe cannot report EOF.
		return true
	}
	return pipeHasWriters(l.read)
}

// Survivors returns the descendants still running, from a fresh walk.
func (l *Lineage) Survivors() ([]int, error) {
	if l == nil {
		return nil, nil
	}
	if err := scanAll(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil, nil
	}
	return append([]int(nil), l.members...), nil
}

// scanAll refreshes every lineage's members in one walk of the process table.
func scanAll() error {
	select {
	case scanGate <- struct{}{}:
		defer func() { <-scanGate }()
	default:
		// A walk is already running; its result answers this caller too.
		scanGate <- struct{}{}
		<-scanGate
		return nil
	}
	registry.mu.Lock()
	candidates := make([]*Lineage, 0, len(registry.live)+len(registry.retired))
	for _, l := range registry.live {
		candidates = append(candidates, l)
	}
	candidates = append(candidates, registry.retired...)
	registry.lastScan = time.Now()
	registry.mu.Unlock()

	handles := make([]uint64, 0, len(candidates))
	scanned := make([]*Lineage, 0, len(candidates))
	for _, l := range candidates {
		l.mu.Lock()
		released, peer := l.released, l.peer
		l.mu.Unlock()
		if released {
			continue
		}
		handles = append(handles, peer)
		scanned = append(scanned, l)
	}
	if len(handles) == 0 {
		return nil
	}
	holders, err := pipeHolders(handles)
	if err != nil {
		return err
	}
	now := time.Now()
	var spent []*Lineage
	for i, l := range scanned {
		l.mu.Lock()
		if !l.released {
			l.members, l.scanned = holders[i], now
		}
		retired := l.state == StateRetired
		l.mu.Unlock()
		// This walk already proved the last descendant is gone, so a retired
		// lineage can give back its descriptor instead of waiting for eviction.
		if retired && len(holders[i]) == 0 {
			spent = append(spent, l)
		}
	}
	for _, l := range spent {
		l.forget()
	}
	return nil
}

// Retire ends the owner's accountability. Descendants keep running and stay
// resolvable so the host can still name where they came from.
func (l *Lineage) Retire() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.released || l.state == StateRetired {
		l.closeWriteLocked()
		l.mu.Unlock()
		return
	}
	l.state = StateRetired
	l.closeWriteLocked()
	l.mu.Unlock()

	registry.mu.Lock()
	delete(registry.live, l.id)
	registry.retired = append(registry.retired, l)
	evict := []*Lineage(nil)
	for len(registry.retired) > retainedRetired {
		evict = append(evict, registry.retired[0])
		registry.retired = registry.retired[1:]
	}
	registry.mu.Unlock()
	for _, old := range evict {
		old.release()
	}
}

// release drops the host's last reference. The lineage resolves nothing after it.
func (l *Lineage) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	l.closeWriteLocked()
	if l.read != nil {
		_ = l.read.Close()
		l.read = nil
	}
	l.members = nil
}

// Close retires the lineage and releases it once no descendant survives.
func (l *Lineage) Close() error {
	if l == nil {
		return nil
	}
	l.Retire()
	if !l.Live() {
		l.forget()
	}
	return nil
}

func (l *Lineage) forget() {
	registry.mu.Lock()
	for i, candidate := range registry.retired {
		if candidate == l {
			registry.retired = append(registry.retired[:i], registry.retired[i+1:]...)
			break
		}
	}
	registry.mu.Unlock()
	l.release()
}

// Of returns the lineage owning pid, live or retired. A pid no recent member set
// names forces one fresh walk before the host concludes it owns nobody.
func Of(pid int) (*Lineage, bool) {
	if pid <= 0 || !supported {
		return nil, false
	}
	if l, ok := holderOf(pid); ok {
		return l, true
	}
	if err := scanAll(); err != nil {
		return nil, false
	}
	return holderOf(pid)
}

// holderOf answers from member sets young enough to trust. An older set can name
// a pid the kernel has since handed to an unrelated process.
func holderOf(pid int) (*Lineage, bool) {
	registry.mu.Lock()
	candidates := make([]*Lineage, 0, len(registry.live)+len(registry.retired))
	for _, l := range registry.live {
		candidates = append(candidates, l)
	}
	candidates = append(candidates, registry.retired...)
	registry.mu.Unlock()
	// A live owner answers before a retired one for a shared descendant.
	for _, l := range candidates {
		l.mu.Lock()
		members, released, fresh := l.members, l.released, time.Since(l.scanned) < memberTTL
		l.mu.Unlock()
		if released || !fresh {
			continue
		}
		for _, member := range members {
			if member == pid {
				return l, true
			}
		}
	}
	return nil, false
}

// OfPeer returns the lineage owning the process on the far end of a loopback
// connection, given the listener's own address and the client address.
func OfPeer(local, remote netip.AddrPort) (*Lineage, bool) {
	if !supported {
		return nil, false
	}
	pid, ok := peerPID(local, remote)
	if !ok {
		return nil, false
	}
	return Of(pid)
}

// Listener is one listening TCP socket and the processes holding it. A holder
// the host cannot inspect is absent from PIDs.
type Listener struct {
	Addr netip.AddrPort
	PIDs []int
}

// Listeners reads the host's listening TCP sockets from the kernel. Platforms
// without a native socket table return ErrUnsupported.
func Listeners() ([]Listener, error) { return listeners() }

// Supported reports whether this platform can observe descendants. A platform
// that cannot must not offer mediated egress, because it cannot attribute one.
func Supported() bool { return supported }

func newID() ID {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// A lineage without a distinct identity cannot attribute anything.
		panic("lineage identity: " + err.Error())
	}
	return ID(hex.EncodeToString(b))
}
