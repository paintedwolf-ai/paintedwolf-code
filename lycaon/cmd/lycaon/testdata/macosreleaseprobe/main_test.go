//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
)

func TestClassifyRefusal(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want probeErrorClassification
	}{
		{name: "success"},
		{name: "entitlement", err: fmt.Errorf("wrapped: %w", errors.Join(credentialstore.ErrCustodianUnavailable, credentialstore.KeychainStatus(-34018))), want: probeErrorClassification{Failed: true, HasKeychainOSStatus: true, KeychainOSStatus: -34018, IsCustodianUnavail: true}},
		{name: "missing identity", err: fmt.Errorf("wrapped: %w", credentialstore.ErrIdentityMissing), want: probeErrorClassification{Failed: true, IsIdentityMissing: true}},
		{name: "missing file", err: fmt.Errorf("wrapped: %w", os.ErrNotExist), want: probeErrorClassification{Failed: true, IsNotExist: true}},
		{name: "other", err: errors.New("keychain OSStatus -34018"), want: probeErrorClassification{Failed: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRefusal(tc.err); got != tc.want {
				t.Fatalf("classification = %+v, want %+v", got, tc.want)
			}
		})
	}
}
