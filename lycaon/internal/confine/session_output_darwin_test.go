//go:build darwin

package confine_test

import "testing"

func TestSessionOutputSeatbelt(t *testing.T) { sessionOutputKernelProof(t, requireSeatbelt(t)) }
