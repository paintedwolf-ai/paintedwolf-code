package httpaction

import (
	"mime"
	"net/http"

	"github.com/lycaon/lycaon/internal/outboundhttp"
)

// remotePage is a successful remote GET that returned an HTML document.
// Loopback and socket targets are services under test, not pages to read.
func remotePage(spec requestSpec, resp outboundhttp.Response) bool {
	if spec.method != http.MethodGet || spec.socket != "" || spec.dialsLoopback() {
		return false
	}
	if resp.Status < 200 || resp.Status > 299 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(resp.ContentType)
	return err == nil && (mediaType == "text/html" || mediaType == "application/xhtml+xml")
}
