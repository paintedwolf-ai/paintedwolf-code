// Package sinks plants recognised secrets in a project and checks that none
// reaches a place the product writes.
package sinks

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"
)

func TestMain(m *testing.M) {
	os.Exit(wiring.RunHostTests(m))
}
