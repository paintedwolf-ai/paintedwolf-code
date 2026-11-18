// Package sinks plants recognised secrets in a project and checks that none
// reaches a place the product writes.
package sinks

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(wiring.Main(m))
}
