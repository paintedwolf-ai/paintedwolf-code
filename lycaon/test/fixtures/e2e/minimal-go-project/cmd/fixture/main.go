package main

import (
	"log"

	"github.com/painted-wolf/e2e-fixture/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
