package webresearch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
)

// TransportFailureKind categorizes low-level network failures without query parameters.
type TransportFailureKind string

const (
	TransportFailureTimeout  TransportFailureKind = "timeout"
	TransportFailureDNS      TransportFailureKind = "dns"
	TransportFailureRefused  TransportFailureKind = "refused"
	TransportFailureTLS      TransportFailureKind = "tls"
	TransportFailureCanceled TransportFailureKind = "canceled"
	TransportFailureOther    TransportFailureKind = "transport"
)

// ProviderTransportError masks raw *url.Error text to prevent query parameter/secret leakage.
type ProviderTransportError struct {
	Kind   TransportFailureKind
	Target string
	Cause  error
}

func (e *ProviderTransportError) Error() string {
	if e == nil {
		return ""
	}
	if e.Target != "" {
		return fmt.Sprintf("%s connecting to %s", e.Kind, e.Target)
	}
	return string(e.Kind)
}

func (e *ProviderTransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func sanitizeProviderHTTPError(req *http.Request, err error) error {
	if err == nil {
		return nil
	}
	var existing *ProviderTransportError
	if errors.As(err, &existing) {
		return existing
	}
	target := ""
	if req != nil && req.URL != nil {
		target = req.URL.Scheme + "://" + req.URL.Host
	}
	var urlErr *url.Error
	cause := err
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		cause = urlErr.Err
	}
	kind := classifyTransportFailure(cause, req)
	return &ProviderTransportError{
		Kind:   kind,
		Target: target,
		Cause:  cause,
	}
}

func classifyTransportFailure(err error, req *http.Request) TransportFailureKind {
	if req != nil && req.Context() != nil {
		if errors.Is(req.Context().Err(), context.Canceled) {
			return TransportFailureCanceled
		}
		if errors.Is(req.Context().Err(), context.DeadlineExceeded) {
			return TransportFailureTimeout
		}
	}
	if errors.Is(err, context.Canceled) {
		return TransportFailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return TransportFailureTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return TransportFailureTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return TransportFailureDNS
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return TransportFailureRefused
	}
	var tlsRecordErr tls.RecordHeaderError
	if errors.As(err, &tlsRecordErr) {
		return TransportFailureTLS
	}
	var certErr x509.CertificateInvalidError
	if errors.As(err, &certErr) {
		return TransportFailureTLS
	}
	var authErr x509.UnknownAuthorityError
	if errors.As(err, &authErr) {
		return TransportFailureTLS
	}
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		return TransportFailureTLS
	}
	return TransportFailureOther
}
