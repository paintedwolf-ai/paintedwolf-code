package main

import (
	"flag"
	"fmt"
	"strings"
)

func parseStrictFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
	}
	return nil
}
