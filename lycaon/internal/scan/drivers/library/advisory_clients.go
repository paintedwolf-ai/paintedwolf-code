package library

import (
	"net/http"
	"time"

	pluginconfig "github.com/google/osv-scalibr/plugin/config"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/httpclient"
)

// advisoryHTTPClients supplies the local OSV matcher with the host's download
// policy instead of the upstream environment-only default transport.
type advisoryHTTPClients struct {
	*pluginconfig.DefaultClientFactories
	http *http.Client
}

func advisoryClients() *advisoryHTTPClients {
	return &advisoryHTTPClients{
		DefaultClientFactories: pluginconfig.NewDefaultClientFactories("Painted Wolf Code"),
		http: httpclient.Download(egressclass.OSVAdvisoryDownload, 30*time.Minute),
	}
}

func (clients *advisoryHTTPClients) HTTPClient() *http.Client { return clients.http }
