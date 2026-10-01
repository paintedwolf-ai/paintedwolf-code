package ollama

import (
	"net/http"
	"time"
)

func responseHeaderTimeoutOf(client *http.Client) time.Duration {
	if client == nil || client.Transport == nil {
		return 0
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		return 0
	}
	return tr.ResponseHeaderTimeout
}
