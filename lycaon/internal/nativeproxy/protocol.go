// Package nativeproxy resolves one native HTTP target through host proxy policy.
// It is a private, co-shipped child protocol; credentials stay in its output pipe.
package nativeproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/systemproxy"
)

const (
	formatVersion   = 1
	maxRequestBytes = 8 << 10
	maxProxyBytes   = 32 << 10
)

type request struct {
	FormatVersion int    `json:"format_version"`
	Target        string `json:"target"`
}

type response struct {
	FormatVersion int     `json:"format_version"`
	Target        string  `json:"target"`
	ProxyURL      *string `json:"proxy_url"`
	ErrorCode     string  `json:"error_code,omitempty"`
}

// Serve accepts exactly one bounded request and never prints resolver diagnostics.
func Serve(input io.Reader, output io.Writer) error {
	return serve(input, output, httpclient.ResolveProxy)
}

func serve(input io.Reader, output io.Writer, resolve func(*url.URL) (*url.URL, error)) error {
	result := response{FormatVersion: formatVersion}
	raw, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil || len(raw) > maxRequestBytes {
		result.ErrorCode = "invalid_request"
		return json.NewEncoder(output).Encode(result)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var requested request
	if err := decoder.Decode(&requested); err != nil || requested.FormatVersion != formatVersion {
		result.ErrorCode = "invalid_request"
		return json.NewEncoder(output).Encode(result)
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		result.ErrorCode = "invalid_request"
		return json.NewEncoder(output).Encode(result)
	}
	result.Target = requested.Target
	target, err := url.Parse(requested.Target)
	if err != nil || len(requested.Target) > 4096 || target.Hostname() == "" || target.User != nil || target.Fragment != "" || (target.Scheme != "https" && target.Scheme != "http") {
		result.ErrorCode = "invalid_target"
		return json.NewEncoder(output).Encode(result)
	}
	proxy, err := resolve(target)
	if err != nil {
		result.ErrorCode = "proxy_resolution_failed"
		var unsupported *systemproxy.Unsupported
		if errors.As(err, &unsupported) {
			result.ErrorCode = "automatic_proxy_unsupported"
		}
	} else if proxy != nil {
		value := proxy.String()
		if len(value) > maxProxyBytes {
			result.ErrorCode = "proxy_endpoint_too_large"
		} else {
			result.ProxyURL = &value
		}
	}
	return json.NewEncoder(output).Encode(result)
}
