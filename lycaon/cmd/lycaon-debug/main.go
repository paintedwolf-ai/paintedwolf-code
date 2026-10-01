// Debug tooling.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/logscli"
)

const usage = `usage: lycaon-debug {logs|eval|bundle-verify|authz|decide} …

Maintainer debug tooling:
  logs           capture log viewer (same as pw logs / pw-logs)
  eval           outcome evaluation and captured application evidence
  bundle-verify  structural + signing audit of a built macOS .app/.dmg
  authz          verify, inspect, or export durable authorization evidence
  decide         export turn load receipts with observed labels as training rows`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "lycaon-debug: %v\n", err)

		var coded exitCodeError
		if errors.As(err, &coded) {
			os.Exit(coded.code)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	switch args[0] {
	case "logs":
		return logscli.Run("lycaon-debug logs", args[1:])
	case "eval":
		return runEval(args[1:])
	case "bundle-verify":
		return runBundleVerify(args[1:])
	case "authz":
		return runAuthz(args[1:])
	case "decide":
		return runDecide(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}
