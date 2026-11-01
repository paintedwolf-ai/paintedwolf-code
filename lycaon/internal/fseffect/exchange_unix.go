//go:build darwin || linux

package fseffect

import (
	"errors"

	"github.com/lycaon/lycaon/internal/fspath"
)

// ExchangeGuarded swaps two identified entries without an absent-name window.
// The caller must exclude concurrent mutation until both directories are synced.
func ExchangeGuarded(root, first, second, firstIdentity, secondIdentity string) error {
	if firstIdentity == "" || secondIdentity == "" {
		return ErrPostcondition
	}
	from, err := openParent(Location{Root: root, Rel: first}, false, 0)
	if err != nil {
		return err
	}
	defer from.close()
	to, err := openParent(Location{Root: root, Rel: second}, false, 0)
	if err != nil {
		return err
	}
	defer to.close()
	for _, check := range []struct {
		parent   *openedParent
		identity string
	}{{from, firstIdentity}, {to, secondIdentity}} {
		identity, err := fspath.EntryIdentityAt(check.parent.fd, check.parent.name)
		if err != nil {
			return err
		}
		if identity != check.identity {
			return ErrPostcondition
		}
		if err := refuseSymlinkTarget(check.parent); err != nil {
			return err
		}
	}
	return exchangeParents(from, to, fsyncFD)
}

// Keep both directory flushes observable even if the first one fails.
func exchangeParents(from, to *openedParent, syncDirectory func(int) error) error {
	if err := renameExchange(from.fd, from.name, to.fd, to.name); err != nil {
		return err
	}
	return errors.Join(syncDirectory(from.fd), syncDirectory(to.fd))
}
