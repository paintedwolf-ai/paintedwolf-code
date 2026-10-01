package providerhttp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/httpclient"
)

func TestReadCompletionResponseRejectsOversizedProviderBody(t *testing.T) {
	resp := &http.Response{
		Body:          io.NopCloser(strings.NewReader("")),
		ContentLength: MaxCompletionResponseBodyBytes.Int64() + 1,
	}

	_, err := ReadCompletionResponse("test provider", resp)
	if !errors.Is(err, httpclient.ErrResponseBodyTooLarge) {
		t.Fatalf("error = %v, want completion response-body limit", err)
	}
	if !strings.Contains(err.Error(), "test provider: read response") {
		t.Fatalf("error lost provider context: %v", err)
	}
}

func TestDecodeDiscoveryResponseRejectsOversizedMetadata(t *testing.T) {
	resp := &http.Response{
		Body:          io.NopCloser(strings.NewReader(`{"data":[]}`)),
		ContentLength: MaxDiscoveryResponseBodyBytes.Int64() + 1,
	}
	var out struct {
		Data []string `json:"data"`
	}
	err := DecodeDiscoveryResponse("test discovery", resp, &out)
	if !errors.Is(err, httpclient.ErrResponseBodyTooLarge) {
		t.Fatalf("error = %v, want discovery response-body limit", err)
	}
	if !strings.Contains(err.Error(), "test discovery: read metadata response") {
		t.Fatalf("error lost discovery context: %v", err)
	}
}
