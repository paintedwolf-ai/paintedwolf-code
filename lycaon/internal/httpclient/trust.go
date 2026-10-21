package httpclient

import (
	"crypto/x509"
	"os"
	"runtime"
)

// environmentTrustKeys are the variables crypto/x509 reads on Linux to replace
// the system trust store.
var environmentTrustKeys = []string{"SSL_CERT_FILE", "SSL_CERT_DIR"}

// PinSystemTrust loads this process's TLS roots from the operating system's
// store, ignoring trust anchors named in the inherited environment, which any
// same-user process can set for a session. The variables are restored
// afterwards, so child processes still receive them; crypto/x509 reads them
// once per process, so later changes do not reach this process's roots. On
// macOS and Windows the platform verifier already ignores them.
func PinSystemTrust() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	saved := map[string]string{}
	for _, key := range environmentTrustKeys {
		if value, ok := os.LookupEnv(key); ok {
			saved[key] = value
			if err := os.Unsetenv(key); err != nil {
				return err
			}
		}
	}
	_, loadErr := x509.SystemCertPool()
	for key, value := range saved {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return loadErr
}
