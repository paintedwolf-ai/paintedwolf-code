// Package httpcookies is the RFC 6265 storage model behind a managed cookie
// jar: which cookies a response may set for a host, and which stored cookies a
// request to a URL carries. It implements net/http's CookieJar so the client
// sends and stores on every hop, and is snapshottable so a jar persists as a
// protected value.
package httpcookies

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// Cookie is one stored cookie. Domain is the canonical host it was set for,
// without a leading dot; HostOnly says whether subdomains are excluded.
type Cookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`
	HostOnly bool      `json:"host_only,omitempty"`
	Path     string    `json:"path"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"http_only,omitempty"`
	SameSite string    `json:"same_site,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
	Created  time.Time `json:"created"`
}

// Store holds the cookies of one jar and counts the traffic through it.
type Store struct {
	mu      sync.Mutex
	cookies []Cookie
	now     func() time.Time
	sent    int
	stored  int
	changed bool
	changes []cookieChange
}

// New loads unexpired cookies without marking the store dirty.
func New(cookies []Cookie) *Store {
	s := &Store{now: time.Now}
	for _, c := range cookies {
		if c.expired(s.now()) {
			continue
		}
		if c.Path == "" {
			c.Path = "/"
		}
		s.cookies = append(s.cookies, c)
	}
	return s
}

// document is the persisted jar. The envelope keeps an empty jar a valid,
// non-trivial value.
type document struct {
	Cookies []Cookie `json:"cookies"`
}

// Decode parses a persisted jar. An empty document is an empty jar.
func Decode(raw []byte) ([]Cookie, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc.Cookies, nil
}

// Encode serializes the live, unexpired cookies in a stable order.
func (s *Store) Encode() ([]byte, error) {
	return json.Marshal(document{Cookies: s.Snapshot()})
}

// Snapshot returns the unexpired cookies sorted by domain, path, and name.
func (s *Store) Snapshot() []Cookie {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]Cookie, 0, len(s.cookies))
	for _, c := range s.cookies {
		if !c.expired(now) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Values returns every stored cookie value, for exact-match screening.
func (s *Store) Values() []string {
	live := s.Snapshot()
	out := make([]string, 0, len(live))
	for _, c := range live {
		out = append(out, c.Value)
	}
	return out
}

// Names returns the stored cookie names in snapshot order. A name is not a
// credential, so a receipt may carry it.
func (s *Store) Names() []string {
	live := s.Snapshot()
	out := make([]string, 0, len(live))
	for _, c := range live {
		out = append(out, c.Name)
	}
	return out
}

// NamesForURL returns only names eligible for this destination, without counting
// a send or exposing values. It shares Lookup's domain/path/security selection.
func (s *Store) NamesForURL(u *url.URL) []string {
	matched := s.matching(u)
	seen := make(map[string]bool, len(matched))
	out := make([]string, 0, len(matched))
	for _, cookie := range matched {
		if !seen[cookie.Name] {
			out = append(out, cookie.Name)
			seen[cookie.Name] = true
		}
	}
	sort.Strings(out)
	return out
}

// Lookup returns the value of the cookie named name that a request to u would
// carry, choosing the same cookie the request would send first.
func (s *Store) Lookup(u *url.URL, name string) (string, bool) {
	for _, c := range s.matching(u) {
		if c.Name == name {
			return c.Value, true
		}
	}
	return "", false
}

// Counts reports cookies sent on requests and stored from responses since New.
func (s *Store) Counts() (sent, stored int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent, s.stored
}

// Changed reports whether the persisted form would differ from what was loaded.
func (s *Store) Changed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// matching is RFC 6265 §5.4 selection: the cookies a request to u carries,
// longest path first, then oldest first. It does not count the traffic.
func (s *Store) matching(u *url.URL) []Cookie {
	if u == nil {
		return nil
	}
	host := canonicalHost(u)
	if host == "" {
		return nil
	}
	secure := strings.EqualFold(u.Scheme, "https")
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var matched []Cookie
	for _, c := range s.cookies {
		if c.expired(now) || (c.Secure && !secure) || !c.domainMatches(host) || !pathMatches(path, c.Path) {
			continue
		}
		matched = append(matched, c)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if len(matched[i].Path) != len(matched[j].Path) {
			return len(matched[i].Path) > len(matched[j].Path)
		}
		return matched[i].Created.Before(matched[j].Created)
	})
	return matched
}

// Cookies implements http.CookieJar.
func (s *Store) Cookies(u *url.URL) []*http.Cookie {
	matched := s.matching(u)
	out := make([]*http.Cookie, 0, len(matched))
	for _, c := range matched {
		// Only name and value travel on a request; the attributes are the ones
		// the origin set, so the cookie describes itself the way it was stored.
		out = append(out, &http.Cookie{ //nolint:gosec // G124 grades response attributes; a request Cookie header carries none
			Name: c.Name, Value: c.Value, Secure: c.Secure, HttpOnly: c.HTTPOnly, SameSite: sameSiteMode(c.SameSite),
		})
	}
	s.mu.Lock()
	s.sent += len(out)
	s.mu.Unlock()
	return out
}

// SetCookies implements http.CookieJar: it stores the cookies a response from u
// set, applying RFC 6265 domain, path, and expiry rules. A cookie for a
// domain the host is not inside, or for a public suffix, is ignored.
func (s *Store) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if u == nil || len(cookies) == 0 {
		return
	}
	host := canonicalHost(u)
	if host == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, incoming := range cookies {
		if incoming == nil || incoming.Name == "" {
			continue
		}
		c, remove, ok := s.storable(host, u, incoming, now)
		if !ok {
			continue
		}
		s.changes = append(s.changes, cookieChange{cookie: c, remove: remove})
		s.replace(c, remove, now)
	}
}

// storable maps one Set-Cookie onto the storage model.
func (s *Store) storable(host string, u *url.URL, in *http.Cookie, now time.Time) (Cookie, bool, bool) {
	c := Cookie{Name: in.Name, Value: in.Value, Secure: in.Secure, HTTPOnly: in.HttpOnly, Created: now}
	domain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(in.Domain), "."))
	// RFC 6265 §5.3-6: a non-empty Domain attribute clears the host-only flag,
	// even when it names the request host, so the cookie reaches subdomains.
	switch {
	case domain == "":
		c.Domain, c.HostOnly = host, true
	case net.ParseIP(host) != nil:
		return Cookie{}, false, false
	case !domainMatches(host, domain):
		return Cookie{}, false, false
	case isPublicSuffix(domain):
		return Cookie{}, false, false
	default:
		c.Domain = domain
	}
	if strings.HasPrefix(in.Path, "/") {
		c.Path = in.Path
	} else {
		c.Path = defaultPath(u.EscapedPath())
	}
	if !prefixSatisfied(c, u) {
		return Cookie{}, false, false
	}
	switch in.SameSite {
	case http.SameSiteLaxMode:
		c.SameSite = "lax"
	case http.SameSiteStrictMode:
		c.SameSite = "strict"
	case http.SameSiteNoneMode:
		c.SameSite = "none"
	default:
	}
	remove := false
	switch {
	case in.MaxAge < 0:
		remove = true
	case in.MaxAge > 0:
		c.Expires = now.Add(time.Duration(in.MaxAge) * time.Second)
	case !in.Expires.IsZero():
		c.Expires = in.Expires.UTC()
		remove = !c.Expires.After(now)
	}
	return c, remove, true
}

func (s *Store) replace(c Cookie, remove bool, now time.Time) {
	kept := s.cookies[:0]
	var previous *Cookie
	for _, existing := range s.cookies {
		if existing.Name == c.Name && existing.Domain == c.Domain && existing.Path == c.Path {
			match := existing
			previous = &match
			c.Created = existing.Created
			continue
		}
		if existing.expired(now) {
			s.changed = true
			continue
		}
		kept = append(kept, existing)
	}
	s.cookies = kept
	if remove {
		// Deleting a cookie the jar never held changes nothing to persist.
		s.changed = s.changed || previous != nil
		return
	}
	s.cookies = append(s.cookies, c)
	s.stored++
	// A replay of the cookie already held is traffic, not a change.
	s.changed = s.changed || previous == nil || !previous.samePersistedForm(c)
}

// samePersistedForm reports whether two cookies would encode identically.
// Created is excluded: it is carried forward from the cookie being replaced.
func (c Cookie) samePersistedForm(other Cookie) bool {
	return c.Value == other.Value && c.HostOnly == other.HostOnly && c.Secure == other.Secure &&
		c.HTTPOnly == other.HTTPOnly && c.SameSite == other.SameSite && c.Expires.Equal(other.Expires)
}

// prefixSatisfied applies the RFC 6265bis §4.1.3 name prefixes. A cookie whose
// name claims a guarantee its attributes do not give is dropped.
func prefixSatisfied(c Cookie, u *url.URL) bool {
	secureOrigin := strings.EqualFold(u.Scheme, "https")
	switch {
	case strings.HasPrefix(c.Name, "__Host-"):
		return secureOrigin && c.Secure && c.HostOnly && c.Path == "/"
	case strings.HasPrefix(c.Name, "__Secure-"):
		return secureOrigin && c.Secure
	default:
		return true
	}
}

func (c Cookie) expired(now time.Time) bool {
	return !c.Expires.IsZero() && !c.Expires.After(now)
}

func (c Cookie) domainMatches(host string) bool {
	if c.HostOnly {
		return host == c.Domain
	}
	return domainMatches(host, c.Domain)
}

// domainMatches is RFC 6265 §5.1.3: host is domain or a subdomain of it, and
// host is not an IP address.
func domainMatches(host, domain string) bool {
	if host == domain {
		return true
	}
	return net.ParseIP(host) == nil && strings.HasSuffix(host, "."+domain)
}

func isPublicSuffix(domain string) bool {
	suffix, _ := publicsuffix.PublicSuffix(domain)
	return suffix == domain
}

// pathMatches is RFC 6265 §5.1.4.
func pathMatches(requestPath, cookiePath string) bool {
	if requestPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	return strings.HasSuffix(cookiePath, "/") || requestPath[len(cookiePath)] == '/'
}

// defaultPath is RFC 6265 §5.1.4: the request path up to its last slash.
func defaultPath(requestPath string) string {
	if requestPath == "" || !strings.HasPrefix(requestPath, "/") {
		return "/"
	}
	idx := strings.LastIndex(requestPath, "/")
	if idx <= 0 {
		return "/"
	}
	return requestPath[:idx]
}

func sameSiteMode(value string) http.SameSite {
	switch value {
	case "lax":
		return http.SameSiteLaxMode
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteDefaultMode
	}
}

func canonicalHost(u *url.URL) string {
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return strings.TrimSuffix(host, ".")
}
