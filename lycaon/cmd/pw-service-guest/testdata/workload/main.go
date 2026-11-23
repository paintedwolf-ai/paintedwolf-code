// Command workload exercises process and stream handling in an isolated test guest.
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	_ "github.com/lycaon/lycaon/internal/confine"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "streams":
		fmt.Fprintf(os.Stderr, "uid=%d gid=%d", os.Getuid(), os.Getgid())
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(1)
		}
		os.Exit(7)
	case "hold":
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(2)
	}
}
