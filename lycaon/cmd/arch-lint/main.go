// Command arch-lint finds structural defects the compiler accepts: code no
// shipped program reaches, forwarders, dropped fields, test-support imports,
// unreferenced commands and Tauri commands Den never invokes.
//
//	arch-lint reach [flags] <main packages>   whole-program reachability, JSON
//	arch-lint vet [flags] <packages>           per-package analyzers, JSON
//	arch-lint refs -root <repo>               commands and scripts nothing references, JSON
//	arch-lint tauri -root <repo>              Tauri commands without a caller or permission, JSON
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: arch-lint reach|vet|refs|tauri ...")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "reach":
		err = runReach(args, os.Stdout)
	case "vet":
		err = runVet(args, os.Stdout)
	case "refs":
		err = runRefs(args, os.Stdout)
	case "tauri":
		err = runTauri(args, os.Stdout)
	default:
		err = fmt.Errorf("unknown subcommand %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "arch-lint:", err)
		os.Exit(2)
	}
}
