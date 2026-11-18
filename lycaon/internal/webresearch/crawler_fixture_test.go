package webresearch

import (
	"context"
	"net/netip"
	"time"

	"github.com/lycaon/lycaon/internal/outboundhttp"
)

func newTestCrawler() *Crawler {
	crawler := NewCrawler(func(ctx context.Context, request outboundhttp.Request) (outboundhttp.Response, error) {
		allowed := request.AllowAddress
		request.AllowAddress = func(addr netip.Addr, port uint16) bool {
			return addr.Unmap().IsLoopback() || (allowed != nil && allowed(addr, port))
		}
		return outboundhttp.Do(ctx, request)
	})
	crawler.interval = 5 * time.Millisecond
	return crawler
}

func loopbackOnlyExchange(ctx context.Context, request outboundhttp.Request) (outboundhttp.Response, error) {
	request.AllowAddress = func(addr netip.Addr, _ uint16) bool { return addr.Unmap().IsLoopback() }
	return outboundhttp.Do(ctx, request)
}
