package hostcmd_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

func TestCommandLineRetainsSequenceIdentity(t *testing.T) {
	for _, connector := range []string{"|", "&&", "||", ";"} {
		got := hostcmd.CommandLine([]hostcmd.StageResult{{Command: "check 'two  words'"}, {Command: "report", Connector: connector}})
		want := "check 'two  words' " + connector + " report"
		if got != want || (connector != "|" && !commandsurface.SameCommandLine(got, want)) {
			t.Fatalf("command=%q want %q", got, want)
		}
	}
}

func TestCommandLineOmitsEmptyStages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stages []hostcmd.StageResult
		want   string
	}{
		{name: "empty"},
		{name: "blank", stages: []hostcmd.StageResult{{Command: " \n"}}},
		{name: "leading blank", stages: []hostcmd.StageResult{{}, {Command: " check ", Connector: "&&"}}, want: "check"},
		{name: "trailing blank", stages: []hostcmd.StageResult{{Command: "check"}, {}}, want: "check"},
		{name: "interior blank", stages: []hostcmd.StageResult{{Command: "check"}, {}, {Command: "report", Connector: ";"}}, want: "check ; report"},
		{name: "implicit pipe", stages: []hostcmd.StageResult{{Command: "check"}, {Command: "report"}}, want: "check | report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostcmd.CommandLine(tc.stages); got != tc.want {
				t.Fatalf("command=%q want %q", got, tc.want)
			}
		})
	}
}
