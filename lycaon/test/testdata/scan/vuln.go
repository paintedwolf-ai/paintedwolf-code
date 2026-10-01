// Package scanfixture contains source-only scanner fixtures.
package scanfixture

import "crypto/tls"

func UnverifiedTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true}
}
