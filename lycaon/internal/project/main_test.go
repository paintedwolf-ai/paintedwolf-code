package project

import (
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"os"
	"testing"
)

func TestMain(m *testing.M) { guidancetestsetup.Install(); gittestsetup.Enable(); os.Exit(m.Run()) }
