// Package providerhttp supplies classified HTTP clients and bounded provider response
// decoding for discovery and completion transports.
package providerhttp

import (
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/httpclient"
)

func RequestClient() *http.Client {
	return httpclient.Streaming(egressclass.LLMProviderRequest)
}

func DiscoveryClient(timeout time.Duration) *http.Client {
	return httpclient.Bounded(egressclass.ProviderModelDiscovery, timeout)
}

// NewStreamingClient bounds connection setup without limiting body duration.
func NewStreamingClient(headerTimeout time.Duration) *http.Client {
	return httpclient.StreamingWithHeaderTimeout(egressclass.LLMProviderRequest, headerTimeout)
}
