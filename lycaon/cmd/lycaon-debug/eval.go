package main

import (
	"errors"
	"fmt"
	"os"
)

const evalUsage = `usage: lycaon-debug eval {tool-usage|evidence|cost} [flags]

Outcome review and tool-usage economics. Live modes spend real tokens and are opt-in.

Evidence export (no model calls):
  evidence --capture DIR --session ID [--project DELIVERED_DIR]
  cost --capture DIR

Tool-usage modes:
  replay (zero tokens): --from <capture-dir>
  live (headless):      --allow-live --addr URL against an isolated sidecar with debug capture
  outcome suite:       --allow-live --suite PATH --expect-model ID --label NAME --out PATH
  suite comparison:    --compare baseline-report.json --from candidate-report.json (zero tokens)

Flags:
  --allow-live       explicitly authorize paid calls; never enabled by automated test targets
  --suite PATH       outcome suite YAML; each case gets a fresh, retained project and session
  --cases IDS        comma-separated suite case IDs, in requested order
  --expect-model ID  exact coordinator model required for suite results
  --label NAME       candidate/baseline label for the suite report
  --compare PATH     compare reviewed suite reports offline
  --refresh PATH     update suite metrics from settled captures offline, preserving outcome reviews
  --from PATH       replay an existing debug capture (llm-requests.jsonl + sessions.jsonl)
  --addr URL        live mode sidecar base URL (default: LYCAON_E2E_API_URL / LYCAON_E2E_ADDR)
  --token TOKEN     API bearer token (default: LYCAON_API_TOKEN / LYCAON_E2E_TOKEN)
  --corpus PATH     task corpus YAML (default: test/fixtures/eval/corpus.yaml)
  --project PATH    project root for live runs (default: minimal-go-project fixture)
  --runs N          repeat the corpus N times and aggregate (live only, default: 1; use 3+ for noise bands)
  --timeout D       optional per-task deadline (live only, default: 0, unlimited)
  --capture PATH    capture dir to read after each live run (default: LYCAON_DEBUG_SESSION_DIR)
  --json            emit JSON profile to stdout (default when piped)
  --table           emit human table (default on TTY)
  --out PATH        write JSON profile to file
  --baseline PATH   compare replay output to committed baseline JSON`

func runEval(args []string) error {
	if len(args) == 0 {
		return errors.New(evalUsage)
	}
	switch args[0] {
	case "cost":
		return runEvalCost(args[1:])
	case "evidence":
		return runEvalEvidence(args[1:])
	case "tool-usage":
		return runEvalToolUsage(args[1:])
	default:
		return fmt.Errorf("unknown eval subcommand %q\n%s", args[0], evalUsage)
	}
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
