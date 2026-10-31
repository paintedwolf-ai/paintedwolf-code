package curl

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// verbose renders curl's -v trace from the owner's observations of every
// hop: "*" lines for the connection and TLS summary, ">" for the request as
// written and "<" for the response header block. A credential header shows
// only its scheme; every other value reaches the model through the program's
// output screen like the rest of stderr.
func verbose(ex httpaction.Exchange) string {
	var b strings.Builder
	for _, hop := range ex.Redirects {
		verboseHop(&b, hop.URL, hop.Observed, presentedBlock(hop.StatusLine, hop.Headers, hop.Wire))
	}
	resp := ex.Response
	verboseHop(&b, resp.FinalURL, resp.Observed, presentedBlock(resp.StatusLine, resp.Headers, resp.Wire))
	return b.String()
}

func verboseHop(b *strings.Builder, rawURL string, observed *outboundhttp.HopObservation, received string) {
	if observed == nil {
		return
	}
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Hostname()
	}
	if observed.Remote.IsValid() {
		ip := observed.Remote.Addr().Unmap().String()
		fmt.Fprintf(b, "*   Trying %s...\n", observed.Remote)
		fmt.Fprintf(b, "* Connected to %s (%s) port %d\n", host, ip, observed.Remote.Port())
	}
	if t := observed.TLS; t != nil {
		fmt.Fprintf(b, "* SSL connection using %s / %s\n", strings.Replace(t.Version, "TLS ", "TLSv", 1), t.CipherSuite)
		if t.ALPN != "" {
			fmt.Fprintf(b, "* ALPN: server accepted %s\n", t.ALPN)
		}
		if c := t.PeerCertificate; c != nil {
			fmt.Fprintf(b, "* Server certificate:\n*  subject: %s\n*  expire date: %s\n*  issuer: %s\n", c.Subject, c.ExpiresAt, c.Issuer)
		}
	}
	proto, _, _ := strings.Cut(received, " ")
	if proto == "HTTP/2.0" {
		proto = "HTTP/2"
	}
	fmt.Fprintf(b, "> %s %s %s\r\n", observed.Method, observed.RequestTarget, proto)
	for _, h := range observed.Sent {
		value := h.Value
		if httpaction.CredentialHeader(h.Name) {
			value = httpaction.AuthScheme(value) + " " + secretmatch.RedactedMarker
		}
		fmt.Fprintf(b, "> %s: %s\r\n", h.Name, value)
	}
	b.WriteString("> \r\n")
	for _, line := range strings.SplitAfter(received, "\r\n") {
		if line != "" {
			b.WriteString("< " + line)
		}
	}
}
