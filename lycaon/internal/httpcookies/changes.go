package httpcookies

// Changes records accepted Set-Cookie operations, not a difference between
// snapshots. Deleting an absent cookie and replaying an identical cookie still
// matter when another request has changed the persisted jar in the meantime.
type Changes struct {
	entries []cookieChange
}

type cookieChange struct {
	cookie Cookie
	remove bool
}

// Changes snapshots the response operations, with absolute expiry times fixed
// when each response arrived. It contains no cookies merely read from the jar.
func (s *Store) Changes() Changes {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Changes{entries: append([]cookieChange(nil), s.changes...)}
}

func (c Changes) Empty() bool { return len(c.entries) == 0 }

// Merge applies the response operations to the latest persisted state. The
// caller serializes persistence; writes to the same domain/path/name take
// effect in save order, while unrelated cookies are left untouched.
func (c Changes) Merge(latest []Cookie) *Store {
	store := New(latest)
	now := store.now()
	for _, change := range c.entries {
		store.replace(change.cookie, change.remove || change.cookie.expired(now), now)
	}
	return store
}

// Values includes cookies replaced or deleted by a later response in the same
// exchange, so those observed credentials also enter exact-match screening.
func (c Changes) Values() []string {
	values := make([]string, 0, len(c.entries))
	for _, change := range c.entries {
		values = append(values, change.cookie.Value)
	}
	return values
}
