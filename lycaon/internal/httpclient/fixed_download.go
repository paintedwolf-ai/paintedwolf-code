package httpclient

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
)

// FixedPublicDownload pins the bundled origin for every shard in a transfer.
func FixedPublicDownload(ctx context.Context, class egressclass.ID, total time.Duration) (string, *http.Client, error) {
	origin := egressclass.RequireSingleFixedEndpoint(class, egressclass.HTTPDownload)
	target, err := url.Parse(origin)
	if err != nil {
		return "", nil, err
	}
	lookup, cancel := context.WithTimeout(ctx, DefaultDial)
	defer cancel()
	ips, err := egress.ResolvePublicIPs(lookup, target.Hostname())
	if err != nil {
		return "", nil, err
	}
	client := downloadClient(egress.PinnedTransport(target, ips, 0), total)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return origin, client, nil
}
