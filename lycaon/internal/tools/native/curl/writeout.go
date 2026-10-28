package curl

import (
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/outboundhttp"
)

// writeOut renders -w after the transfer from the owner's observations. A
// run without a response reports 000 and zero facts, as curl does; exit is
// the status curl exits with.
func (inv *invocation) writeOut(exit int) error {
	if !inv.opts.hasWriteOut {
		return nil
	}
	var b strings.Builder
	for _, token := range inv.opts.writeOut {
		if token.variable == "" {
			b.WriteString(token.literal)
			continue
		}
		b.WriteString(inv.writeOutValue(token.variable, exit))
	}
	_, err := io.WriteString(inv.stdout, b.String())
	return err
}

func seconds6(d time.Duration) string { return fmt.Sprintf("%.6f", d.Seconds()) }

func (inv *invocation) writeOutValue(variable string, exit int) string {
	ex := inv.exchange
	if variable == "exitcode" {
		return strconv.Itoa(exit)
	}
	if variable == "url_effective" {
		if ex == nil {
			return inv.opts.url
		}
		return ex.FinalURL
	}
	if ex == nil {
		switch variable {
		case "http_code":
			return "000"
		case "time_total":
			if inv.started.IsZero() {
				return seconds6(0)
			}
			return seconds6(time.Since(inv.started))
		case "time_namelookup", "time_connect", "time_appconnect", "time_starttransfer":
			return seconds6(0)
		case "remote_ip", "content_type", "redirect_url":
			return ""
		}
		return "0"
	}
	resp := ex.Response
	observed := resp.Observed
	if observed == nil {
		observed = &outboundhttp.HopObservation{}
	}
	switch variable {
	case "http_code":
		return fmt.Sprintf("%03d", resp.Status)
	case "size_download":
		return strconv.FormatInt(resp.Bytes, 10)
	case "time_total":
		return seconds6(resp.Elapsed)
	case "time_namelookup":
		return seconds6(observed.NameLookup)
	case "time_connect":
		return seconds6(observed.Connect)
	case "time_appconnect":
		return seconds6(observed.AppConnect)
	case "time_starttransfer":
		return seconds6(observed.StartTransfer)
	case "remote_ip":
		if !observed.Remote.IsValid() {
			return ""
		}
		return observed.Remote.Addr().Unmap().String()
	case "remote_port":
		return strconv.Itoa(int(observed.Remote.Port()))
	case "content_type":
		return resp.ContentType
	case "http_version":
		return httpVersion(resp.StatusLine)
	case "num_redirects":
		return strconv.Itoa(len(ex.Redirects))
	case "redirect_url":
		return redirectURL(ex)
	case "size_upload":
		return strconv.FormatInt(ex.SentBodyBytes, 10)
	case "size_header":
		return strconv.Itoa(len(inv.headers))
	case "speed_download":
		if resp.Elapsed <= 0 {
			return "0"
		}
		return strconv.FormatInt(int64(float64(resp.Bytes)/resp.Elapsed.Seconds()), 10)
	}
	return ""
}

// httpVersion is curl's %{http_version}: 1.0, 1.1, 2 or 3.
func httpVersion(statusLine string) string {
	proto, _, _ := strings.Cut(statusLine, " ")
	switch version := strings.TrimPrefix(proto, "HTTP/"); version {
	case "2.0", "3.0":
		return version[:1]
	default:
		return version
	}
}

// redirectURL is the absolute Location of a redirect the run did not
// follow; curl reports nothing once it follows.
func redirectURL(ex *httpaction.Exchange) string {
	switch ex.Response.Status {
	case 301, 302, 303, 307, 308:
	default:
		return ""
	}
	for _, h := range ex.Response.Headers {
		if strings.EqualFold(h.Name, "Location") {
			base, err := url.Parse(ex.FinalURL)
			ref, refErr := url.Parse(h.Value)
			if err != nil || refErr != nil {
				return h.Value
			}
			return base.ResolveReference(ref).String()
		}
	}
	return ""
}
