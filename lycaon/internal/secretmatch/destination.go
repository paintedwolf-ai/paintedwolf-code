package secretmatch

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/url"
	"sort"
	"strings"
)

// DestinationKey binds a release grant to the resolved transport facts.
func DestinationKey(id string, transportFacts ...string) string {
	id = strings.TrimSpace(id)
	var material strings.Builder
	for _, fact := range transportFacts {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(fact)))
		material.Write(size[:])
		material.WriteString(fact)
	}
	if material.Len() == 0 {
		return id
	}
	sum := sha256.Sum256([]byte(material.String()))
	return id + "@" + hex.EncodeToString(sum[:8])
}

// HTTPOrigin includes scheme, host, and effective port in destination identity.
func HTTPOrigin(u *url.URL) string {
	if u == nil {
		return ""
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	port := strings.TrimSpace(u.Port())
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	if port == "" {
		return scheme + "://" + host
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// HTTPDestination returns the grant identity and the presentation label for a
// resolved HTTP destination. The label is presentation only.
func HTTPDestination(u *url.URL) (id, label string) {
	if u == nil {
		return "", ""
	}
	origin := HTTPOrigin(u)
	return DestinationKey(strings.ToLower(strings.TrimSpace(u.Hostname())), origin), origin
}

// AmbientAuth is the credential configuration a destination applies to every
// request, independent of the payload being screened.
type AmbientAuth struct {
	// Wire and Header name how a credential is presented and are not secret.
	Wire, Header string
	// Token, Headers, and Env carry credential values.
	Token   string
	Headers map[string]string
	Env     map[string]string
}

// authValueUnidentified marks credentials whose keyed identity is unavailable.
const authValueUnidentified = "unidentified"

// Identity binds the destination to credential fields using keyed fingerprints.
func (a AmbientAuth) Identity(m *Matcher) string {
	keyed := func(value string) string {
		if value == "" {
			return ""
		}
		if m == nil || m.fingerprinter == nil {
			return authValueUnidentified
		}
		return string(m.fingerprinter.Fingerprint(value))
	}
	facts := []string{"wire=" + a.Wire, "header=" + a.Header, "token=" + keyed(a.Token)}
	facts = append(facts, namedAuthFacts("header:", a.Headers, keyed)...)
	facts = append(facts, namedAuthFacts("env:", a.Env, keyed)...)
	return strings.Join(facts, "\x00")
}

// namedAuthFacts renders one credential map in a stable order.
func namedAuthFacts(prefix string, values map[string]string, keyed func(string) string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	facts := make([]string, 0, len(names))
	for _, name := range names {
		facts = append(facts, prefix+name+"="+keyed(values[name]))
	}
	return facts
}
