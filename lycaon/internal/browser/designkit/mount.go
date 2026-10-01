package designkit

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// AssetServer fulfills hermetic non-kit origins (the project asset fence).
// TryHandle returns false when the request is not for its origin.
type AssetServer interface {
	TryHandle(h *rod.Hijack) bool
}

// Mount serves bundled documents and fonts without network access.
type Mount struct {
	HTML   string
	assets AssetServer
	router *rod.HijackRouter
}

// AttachMount installs offline routing for KitOrigin. Nil assets disables project files.
func AttachMount(page *rod.Page, html string, assets AssetServer) (*Mount, error) {
	if page == nil {
		return nil, fmt.Errorf("designkit: nil page")
	}
	router := page.HijackRequests()
	m := &Mount{HTML: html, assets: assets, router: router}
	if err := router.Add("*", "", m.handle); err != nil {
		_ = router.Stop()
		return nil, err
	}
	go router.Run()
	return m, nil
}

// Close stops Fetch hijacking.
func (m *Mount) Close() error {
	if m == nil || m.router == nil {
		return nil
	}
	err := m.router.Stop()
	m.router = nil
	return err
}

// ViewURL is the navigation target for a mounted kit document.
func ViewURL() string {
	return strings.TrimRight(KitOrigin, "/") + ViewPath
}

func (m *Mount) handle(h *rod.Hijack) {
	if h == nil || h.Request == nil {
		return
	}
	reqURL := h.Request.URL()
	if reqURL == nil || !sameOrigin(KitOrigin, reqURL.String()) {
		if reqURL != nil && m.assets != nil && m.assets.TryHandle(h) {
			return
		}
		// Offline fence: only hermetic origins are fulfilled during render_view.
		h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
		return
	}
	p := path.Clean("/" + reqURL.EscapedPath())
	switch {
	case p == ViewPath || p == "/":
		h.Response.Payload().ResponseCode = http.StatusOK
		h.Response.SetHeader("Content-Type", "text/html; charset=utf-8")
		h.Response.SetBody(m.HTML)
	case strings.HasPrefix(p, "/fonts/"):
		answerFont(h, kitFontFile(path.Base(p)))
	default:
		h.Response.Payload().ResponseCode = http.StatusNotFound
		h.Response.SetBody("not found")
	}
}

func answerFont(h *rod.Hijack, r FontResponse) {
	if r.Blocked {
		h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
		return
	}
	h.Response.Payload().ResponseCode = r.Status
	for k, v := range r.Headers {
		h.Response.SetHeader(k, v)
	}
	h.Response.SetBody(r.Body)
}

func sameOrigin(baseURL, targetURL string) bool {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	target := strings.TrimSpace(targetURL)
	if base == "" || target == "" {
		return false
	}
	bu, err1 := url.Parse(base)
	tu, err2 := url.Parse(target)
	if err1 != nil || err2 != nil || bu == nil || tu == nil {
		return false
	}
	return strings.EqualFold(bu.Scheme, tu.Scheme) && strings.EqualFold(bu.Host, tu.Host)
}
