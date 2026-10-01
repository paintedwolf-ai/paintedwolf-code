package main

import (
	"context"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/sshproxy"
)

func runSSHProxyCommand(args []string) int {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: pw ssh-proxy-command HOST PORT\n")
		return 2
	}
	ctx := context.Background()
	if err := sshproxy.RunProxyCommand(ctx, args[0], args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "ssh-proxy-command: %v\n", err)
		return 1
	}
	return 0
}
