package socketref

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
)

// Binding names an existing retained object. It never reconstructs authority by
// opening a saved pathname after the engine restarts.
type Binding struct {
	ID          string      `json:"id,omitempty" yaml:"id,omitempty"`
	Enforcement Enforcement `json:"enforcement,omitempty" yaml:"enforcement,omitempty"`
}

// Enforcement distinguishes a kernel pathname rule from a retained destination.
type Enforcement string

const (
	NoRealization  Enforcement = "none"
	KernelPathname Enforcement = "kernel_pathname"
	RetainedObject Enforcement = "retained_object"
)

// Adapter reports the realizations supplied by the platform implementation.
type Adapter struct {
	Command Enforcement
	Native  Enforcement
}

// Mode preserves existing pathname grants only on adapters that realize them.
func (b Binding) Mode() Enforcement { return Platform().BindingMode(b) }

// BindingMode preserves an explicit contract and otherwise uses this adapter.
func (a Adapter) BindingMode(b Binding) Enforcement {
	if b.Enforcement != "" {
		return b.Enforcement
	}
	return a.Command
}

// Key includes the enforcement contract as well as its object identity.
func (b Binding) Key() string { return string(b.Mode()) + "\x00" + b.ID }

// ValidateShape refuses unknown saved interpretations without acquiring a lease.
func (b Binding) ValidateShape() error {
	if b.Enforcement != "" && b.Enforcement != NoRealization && b.Enforcement != KernelPathname && b.Enforcement != RetainedObject {
		return fmt.Errorf("unknown socket binding enforcement %q", b.Enforcement)
	}
	if b.Mode() != RetainedObject && b.ID != "" {
		return fmt.Errorf("socket realization without retained objects carries an object identity")
	}
	return nil
}

// Lease is one owner's share of a binding. Owners close it at cancellation,
// expiry, revocation or transfer. Cleanup is a fallback for abandoned owners.
type Lease struct {
	binding Binding
	mu      sync.Mutex
	closed  bool
	cleanup runtime.Cleanup
}

type bindingEntry struct {
	reference  *Reference
	owners     int
	operations int
}

var bindings = struct {
	sync.Mutex
	refs map[string]*bindingEntry
}{refs: make(map[string]*bindingEntry)}

// Capture gives the caller ownership of a destination before approval.
func Capture(path string) (*Lease, error) {
	ref, err := Open(path)
	if err != nil {
		return nil, err
	}
	var nonce [32]byte
	rand.Read(nonce[:])
	id := hex.EncodeToString(nonce[:])
	bindings.Lock()
	bindings.refs[id] = &bindingEntry{reference: ref, owners: 1}
	bindings.Unlock()
	return newLease(Binding{ID: id, Enforcement: RetainedObject}), nil
}

func newLease(binding Binding) *Lease {
	lease := &Lease{binding: binding}
	lease.cleanup = runtime.AddCleanup(lease, releaseBinding, binding.ID)
	return lease
}

// Binding is the serializable identity passed with the exact grant.
func (l *Lease) Binding() Binding { return l.binding }

// Acquire takes ownership while an existing owner still retains the object.
// Unknown or released IDs require fresh approval; no pathname is consulted.
func (b Binding) Acquire() (*Lease, error) {
	if b.Mode() != RetainedObject {
		return nil, ErrUnbound
	}
	bindings.Lock()
	defer bindings.Unlock()
	entry := bindings.refs[b.ID]
	if entry == nil {
		return nil, ErrUnbound
	}
	entry.owners++
	return newLease(b), nil
}

// Path is the captured object's diagnostic name, not a connection destination.
func (b Binding) Path() (string, error) {
	lease, err := b.Acquire()
	if err != nil {
		return "", err
	}
	defer lease.Close()
	ref, release, err := lease.operation()
	if err != nil {
		return "", err
	}
	defer release()
	return ref.path, nil
}

// MatchesPath reports whether the current name still denotes the retained
// object. This read can mark intent stale; it never captures new authority.
func (b Binding) MatchesPath(path string) bool {
	lease, err := b.Acquire()
	if err != nil {
		return false
	}
	defer lease.Close()
	ref, release, err := lease.operation()
	if err != nil {
		return false
	}
	defer release()
	return matchesPath(ref, path)
}

// SameObject compares two simultaneously retained objects. Holding both objects
// prevents inode reuse; unretained inode metadata never establishes authority.
func (b Binding) SameObject(other Binding) bool {
	first, err := b.Acquire()
	if err != nil {
		return false
	}
	defer first.Close()
	second, err := other.Acquire()
	if err != nil {
		return false
	}
	defer second.Close()
	a, doneA, err := first.operation()
	if err != nil {
		return false
	}
	defer doneA()
	c, doneC, err := second.operation()
	if err != nil {
		return false
	}
	defer doneC()
	return sameObject(a, c)
}

// DialContext holds an operation share until the kernel connect has finished.
func (l *Lease) DialContext(ctx context.Context) (net.Conn, error) {
	ref, release, err := l.operation()
	if err != nil {
		return nil, err
	}
	defer release()
	return ref.DialContext(ctx)
}

// Duplicate transfers a descriptor owned independently by the execution helper.
func (l *Lease) Duplicate() (*os.File, error) {
	ref, release, err := l.operation()
	if err != nil {
		return nil, err
	}
	defer release()
	return ref.Duplicate()
}

func (l *Lease) operation() (*Reference, func(), error) {
	if l == nil {
		return nil, nil, ErrUnbound
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, nil, ErrUnbound
	}
	bindings.Lock()
	defer bindings.Unlock()
	entry := bindings.refs[l.binding.ID]
	if entry == nil {
		return nil, nil, ErrUnbound
	}
	entry.operations++
	return entry.reference, func() { releaseOperation(entry) }, nil
}

// Close deterministically releases this owner's share.
func (l *Lease) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.cleanup.Stop()
	releaseBinding(l.binding.ID)
}

func releaseBinding(id string) {
	bindings.Lock()
	entry := bindings.refs[id]
	if entry == nil {
		bindings.Unlock()
		return
	}
	entry.owners--
	if entry.owners != 0 {
		bindings.Unlock()
		return
	}
	delete(bindings.refs, id)
	closeNow := entry.operations == 0
	bindings.Unlock()
	if closeNow {
		_ = entry.reference.Close()
	}
}

func releaseOperation(entry *bindingEntry) {
	bindings.Lock()
	entry.operations--
	closeNow := entry.owners == 0 && entry.operations == 0
	bindings.Unlock()
	if closeNow {
		_ = entry.reference.Close()
	}
}
