package outboundhttp

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Permanent identifies request failures that retries cannot resolve.
func Permanent(err error) bool {
	var (
		oversized *BodyTooLargeError
		unusable  *RequestInvalidError
		redirect  *RedirectError
		tlsFault  *TLSFault
	)
	return errors.As(err, &oversized) || errors.As(err, &unusable) || errors.As(err, &redirect) || errors.As(err, &tlsFault)
}

// TLSFault records a typed TLS connection or verification failure.
type TLSFault struct {
	Type      string   `json:"type"`
	Reason    string   `json:"reason"`
	Detail    string   `json:"detail,omitempty"`
	Host      string   `json:"host,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	Issuer    string   `json:"issuer,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	ValidFrom string   `json:"valid_from,omitempty"`
	DNSNames  []string `json:"dns_names,omitempty"`
	Err       error    `json:"-"`
}

func (e *TLSFault) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return e.Detail
}
func (e *TLSFault) Unwrap() error { return e.Err }

// ClassifyTLSError converts standard x509 verification failures to a typed TLSFault.
func ClassifyTLSError(err error) *TLSFault {
	if err == nil {
		return nil
	}
	var (
		certInvalid    x509.CertificateInvalidError
		certInvalidPtr *x509.CertificateInvalidError
		unknownAuth    x509.UnknownAuthorityError
		unknownAuthPtr *x509.UnknownAuthorityError
		hostErr        x509.HostnameError
		hostErrPtr     *x509.HostnameError
		sysRoots       x509.SystemRootsError
		sysRootsPtr    *x509.SystemRootsError
	)
	switch {
	case errors.As(err, &certInvalid) || errors.As(err, &certInvalidPtr):
		reason := certInvalid.Reason
		cert := certInvalid.Cert
		errStr := certInvalid.Error()
		if certInvalidPtr != nil {
			reason = certInvalidPtr.Reason
			cert = certInvalidPtr.Cert
			errStr = certInvalidPtr.Error()
		}
		fault := &TLSFault{
			Type:   "invalid_certificate",
			Reason: errStr,
			Detail: errStr,
			Err:    err,
		}
		if reason == x509.Expired {
			if cert != nil && time.Now().Before(cert.NotBefore) {
				fault.Type = "not_yet_valid"
			} else {
				fault.Type = "expired"
			}
		}
		if cert != nil {
			fault.Subject = cert.Subject.String()
			fault.Issuer = cert.Issuer.String()
			fault.ExpiresAt = cert.NotAfter.Format(time.RFC3339)
			fault.ValidFrom = cert.NotBefore.Format(time.RFC3339)
			fault.DNSNames = cert.DNSNames
		}
		return fault
	case errors.As(err, &unknownAuth) || errors.As(err, &unknownAuthPtr):
		cert := unknownAuth.Cert
		errStr := unknownAuth.Error()
		if unknownAuthPtr != nil {
			cert = unknownAuthPtr.Cert
			errStr = unknownAuthPtr.Error()
		}
		fault := &TLSFault{
			Type:   "unknown_authority",
			Reason: errStr,
			Detail: errStr,
			Err:    err,
		}
		if cert != nil {
			fault.Subject = cert.Subject.String()
			fault.Issuer = cert.Issuer.String()
			fault.ExpiresAt = cert.NotAfter.Format(time.RFC3339)
		}
		return fault
	case errors.As(err, &hostErr) || errors.As(err, &hostErrPtr):
		host := hostErr.Host
		cert := hostErr.Certificate
		errStr := hostErr.Error()
		if hostErrPtr != nil {
			host = hostErrPtr.Host
			cert = hostErrPtr.Certificate
			errStr = hostErrPtr.Error()
		}
		fault := &TLSFault{
			Type:   "hostname_mismatch",
			Reason: errStr,
			Detail: errStr,
			Host:   host,
			Err:    err,
		}
		if cert != nil {
			fault.Subject = cert.Subject.String()
			fault.Issuer = cert.Issuer.String()
			fault.DNSNames = cert.DNSNames
		}
		return fault
	case errors.As(err, &sysRoots) || errors.As(err, &sysRootsPtr):
		errStr := sysRoots.Error()
		if sysRootsPtr != nil {
			errStr = sysRootsPtr.Error()
		}
		return &TLSFault{
			Type:   "system_roots_unavailable",
			Reason: errStr,
			Detail: errStr,
			Err:    err,
		}
	}
	return nil
}

// BodyTooLargeError reports a response past the caller's byte bound.
type BodyTooLargeError struct {
	Limit int64
}

func (e *BodyTooLargeError) Error() string {
	return fmt.Sprintf("http response exceeds %d-byte limit", e.Limit)
}

// RequestInvalidError identifies an unusable method, header, or URL.
type RequestInvalidError struct {
	Reason string
}

func (e *RequestInvalidError) Error() string { return e.Reason }

func invalidRequest(format string, args ...any) *RequestInvalidError {
	return &RequestInvalidError{Reason: fmt.Sprintf(format, args...)}
}

// RedirectError records a refused redirect and the hops already followed.
type RedirectError struct {
	Reason string
	Hops   []Hop
}

func (e *RedirectError) Error() string { return e.Reason }

// TransferError distinguishes response read failures from sink failures.
type TransferError struct {
	Err error
}

func (e *TransferError) Error() string { return "response transfer failed: " + e.Err.Error() }

func (e *TransferError) Unwrap() error { return e.Err }

// redirectMethod applies the method and body rules from RFC 9110 §15.4.
func redirectMethod(status int, method string) (next string, keepBody bool) {
	switch status {
	case http.StatusSeeOther:
		if method == http.MethodHead {
			return http.MethodHead, false
		}
		return http.MethodGet, false
	case http.StatusMovedPermanently, http.StatusFound:
		if method == http.MethodPost {
			return http.MethodGet, false
		}
		return method, true
	default:
		return method, true
	}
}

// ExchangeError retains request attempts and received headers on failure.
type ExchangeError struct {
	Method            string
	RequestsStarted   int
	ResponsesReceived int
	LastStatus        int
	Err               error
}

func (e *ExchangeError) Error() string { return e.Err.Error() }
func (e *ExchangeError) Unwrap() error { return e.Err }

