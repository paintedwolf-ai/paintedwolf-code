package curl

import (
	"crypto/tls"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// clientTLS reads --cacert, --capath, --cert and --key through read
// authority into the owner's per-request TLS material. It only adds trust
// and a client identity; verification stays on. The passphrase opens the key
// here and is never written anywhere.
func (inv *invocation) clientTLS() (*outboundhttp.ClientTLS, error) {
	t := inv.opts.tls
	if t == (tlsOptions{}) {
		return nil, nil
	}
	out := &outboundhttp.ClientTLS{}
	var bundles [][]byte
	if t.cacert != "" {
		data, err := inv.readFile(t.cacert)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, data)
	}
	if t.capath != "" {
		data, err := inv.readCAPath(t.capath)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, data)
	}
	if len(bundles) > 0 {
		pool, err := outboundhttp.TrustPool(bundles...)
		if err != nil {
			return nil, materialFailure(77, "error setting certificate verify locations", err)
		}
		out.Roots = pool
	}
	if t.cert != "" {
		certPEM, err := inv.readFile(t.cert)
		if err != nil {
			return nil, err
		}
		var keyPEM []byte
		if t.key != "" {
			if keyPEM, err = inv.readFile(t.key); err != nil {
				return nil, err
			}
		}
		passphrase := t.pass
		if passphrase == "" {
			passphrase = t.certPass
		}
		pair, err := outboundhttp.ClientCertificate(certPEM, keyPEM, passphrase)
		if err != nil {
			return nil, materialFailure(58, "unable to use client certificate", err)
		}
		out.Certificates = []tls.Certificate{pair}
	}
	return out, nil
}

// materialFailure is curl's local-material exit; nothing was sent.
func materialFailure(code int, message string, err error) error {
	var material *outboundhttp.TLSMaterialError
	if errors.As(err, &material) {
		message += ": " + material.Reason
	}
	return &curlFailure{code: code, message: message, certainty: toolcontract.NoEffect, cause: err}
}

// readCAPath concatenates the regular files of a --capath directory, read
// through read authority, as one bundle; non-certificate files add nothing.
func (inv *invocation) readCAPath(operand string) ([]byte, error) {
	bound, err := inv.program.Paths.Bind(inv.ctx, operand, false)
	if err != nil {
		return nil, err
	}
	dir, err := bound.OpenDir()
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	names, err := bound.ReadDir()
	if err != nil {
		return nil, err
	}
	maximum := inv.owners.limits["stream_bytes"]
	var bundle []byte
	for _, name := range names {
		child, _, err := dir.StatChild(name)
		if err != nil {
			return nil, err
		}
		// OpenSSL's hashed names are usually links to the certificate.
		if stat, err := dir.StatChildFollowed(child); err != nil || !stat.Mode.IsRegular() {
			continue
		}
		f, err := child.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, maximum+1-int64(len(bundle))))
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		bundle = append(append(bundle, data...), '\n')
		if int64(len(bundle)) > maximum {
			return nil, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": maximum, "observed": int64(len(bundle))}}
		}
	}
	return bundle, nil
}
