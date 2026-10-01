//go:build !darwin

package credentialstore

import "fmt"

func VerifyIdentityProtection() error {
	return fmt.Errorf("native identity protection verification is available on macOS")
}
