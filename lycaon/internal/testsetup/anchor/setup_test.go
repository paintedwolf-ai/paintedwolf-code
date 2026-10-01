package anchortestsetup_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
)

func TestInstallRequiresAndInstallsBundledRegistry(t *testing.T) {
	anchortestsetup.Install()
	if anchor.DefaultRegistry() == nil {
		t.Fatal("Install left the default registry empty")
	}
}
