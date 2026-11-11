package httpaction

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/outboundhttp"
)

// admitHopRoute gates the nominal hop URL and any explicitly mapped route
// destination selected for that hop before DNS resolution or dialing.
func admitHopRoute(ctx context.Context, hopURL *url.URL, method string, resolve []outboundhttp.ResolveMapping) error {
	if hopURL == nil {
		return nil
	}
	if err := egressgate.AwaitHTTPRequest(ctx, hopURL, method); err != nil {
		return err
	}
	routes, err := selectedHopRouteURLs(hopURL, resolve)
	if err != nil {
		return err
	}
	for _, routeURL := range routes {
		if err := egressgate.AwaitHTTPRequest(ctx, routeURL, method); err != nil {
			return err
		}
	}
	return nil
}

func selectedHopRouteURLs(hopURL *url.URL, resolve []outboundhttp.ResolveMapping) ([]*url.URL, error) {
	if hopURL == nil || len(resolve) == 0 {
		return nil, nil
	}
	port, err := outboundhttp.EffectivePort(hopURL)
	if err != nil {
		return nil, err
	}
	host := strings.TrimSpace(hopURL.Hostname())
	if host == "" {
		return nil, nil
	}
	dial, mapped := outboundhttp.MappedDial(resolve, host, port)
	if !mapped {
		return nil, nil
	}
	var routes []*url.URL
	targetHost := strings.TrimSpace(dial.Host)
	if !dial.Address.IsValid() {
		if targetHost != "" && (!strings.EqualFold(targetHost, host) || dial.Port != port) {
			routes = append(routes, hopRouteURL(hopURL, targetHost, dial.Port))
		}
		return routes, nil
	}
	addedTargetHost := targetHost != "" && !strings.EqualFold(targetHost, host)
	if addedTargetHost {
		routes = append(routes, hopRouteURL(hopURL, targetHost, dial.Port))
	}
	addrHost := dial.Address.Unmap().String()
	if (!strings.EqualFold(addrHost, host) || dial.Port != port) && (!addedTargetHost || !strings.EqualFold(addrHost, targetHost)) {
		routes = append(routes, hopRouteURL(hopURL, addrHost, dial.Port))
	}
	return routes, nil
}

func hopRouteURL(hopURL *url.URL, host string, port uint16) *url.URL {
	route := *hopURL
	trimmed := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(host), "["), "]")
	route.Host = net.JoinHostPort(trimmed, strconv.Itoa(int(port)))
	return &route
}
