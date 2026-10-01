package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/eval/toolusage"
)

func runEvalSuite(flags evalToolUsageFlags) error {
	path, err := toolusage.ResolveModulePath(flags.suite)
	if err != nil {
		return err
	}
	suite, err := toolusage.LoadSuite(path)
	if err != nil {
		return err
	}
	if flags.cases != "" {
		ids := strings.Split(flags.cases, ",")
		for i := range ids {
			ids[i] = strings.TrimSpace(ids[i])
		}
		if err := suite.Select(ids); err != nil {
			return err
		}
	}
	token := flags.token
	if token == "" {
		token = os.Getenv("LYCAON_API_TOKEN")
	}
	if token == "" {
		token = os.Getenv("LYCAON_E2E_TOKEN")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, err := toolusage.RunSuite(ctx, toolusage.SuiteOptions{
		LiveOptions: toolusage.LiveOptions{AllowLive: flags.allowLive, BaseURL: flags.addr, Token: token, Runs: flags.runs, Timeout: flags.timeout, CaptureDir: flags.capture},
		StateDB:     flags.stateDB,
		Suite:       suite, ExpectedModel: flags.expectModel, Label: flags.label, Output: flags.out,
		WaitForInput: flags.waitForInput,
		Progress: func(c toolusage.CaseReport) {
			fmt.Printf("%s run=%d status=%s session=%s checkpoint_requests=%d feedback_requests=%d report=%s\n", c.ID, c.Run, c.Status, c.SessionID, len(c.CheckpointRequests), len(c.FeedbackRequests), flags.out)
		},
	})
	for _, c := range report.Cases {
		fmt.Printf("%s run=%d status=%s duration=%s session=%s\n", c.ID, c.Run, c.Status, fmt.Sprintf("%.1fs", float64(c.DurationMS)/1000), c.SessionID)
	}
	if report.WorkDir != "" {
		fmt.Printf("Report: %s\nRetained projects: %s\nPrompt tokens: %d\nOutcome criteria require review; tool counts are diagnostics.\n", flags.out, report.WorkDir, report.PromptTokens)
	}
	return err
}
