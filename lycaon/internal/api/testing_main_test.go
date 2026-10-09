package api

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
)

// TestMain installs process-global test dependencies.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	guidancetestsetup.Install()
	anchortestsetup.Install()
	defer func() { _ = sourcecatalog.Process().Drain(context.Background()) }()
	code := m.Run()
	_ = sourcecatalog.Process().Drain(context.Background())
	os.Exit(code)
}
