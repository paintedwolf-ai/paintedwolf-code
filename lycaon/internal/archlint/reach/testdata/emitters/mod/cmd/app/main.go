package main

import (
	"fmt"
	"os"

	"example.com/emitters/facts"
	"example.com/emitters/guard"
)

func main() {
	ctx := &facts.Context{}
	ctx.Live = true
	fmt.Println(guard.Reject(), guard.Fire(), facts.Project(ctx), facts.Family("observed"), guard.Seen(os.Getenv("CODE")))
}
