package providerhttp

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/httpclient"
)

const MaxCompletionResponseBodyBytes bytebound.Transport = 64 << 20

const MaxDiscoveryResponseBodyBytes bytebound.Transport = 8 << 20

const MaxCredentialResponseBodyBytes bytebound.Transport = 1 << 20

func ReadCompletionResponse(provider string, resp *http.Response) ([]byte, error) {
	return readProviderResponse(provider, "response", resp, MaxCompletionResponseBodyBytes)
}

func DecodeCompletionResponse(provider string, resp *http.Response, out any) error {
	return decodeProviderResponse(provider, "response", resp, MaxCompletionResponseBodyBytes, out)
}

func DecodeDiscoveryResponse(provider string, resp *http.Response, out any) error {
	return decodeProviderResponse(provider, "metadata response", resp, MaxDiscoveryResponseBodyBytes, out)
}

func DecodeCredentialResponse(provider string, resp *http.Response, out any) error {
	return decodeProviderResponse(provider, "credential response", resp, MaxCredentialResponseBodyBytes, out)
}

func decodeProviderResponse(provider, kind string, resp *http.Response, limit bytebound.Transport, out any) error {
	body, err := readProviderResponse(provider, kind, resp, limit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: decode %s: %w", provider, kind, err)
	}
	return nil
}

func readProviderResponse(provider, kind string, resp *http.Response, limit bytebound.Transport) ([]byte, error) {
	body, err := httpclient.ReadResponseBody(resp, limit)
	if err != nil {
		return nil, fmt.Errorf("%s: read %s: %w", provider, kind, err)
	}
	return body, nil
}
