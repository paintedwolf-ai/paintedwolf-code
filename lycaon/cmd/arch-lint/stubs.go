package main

import (
	"errors"
	"io"
)

func runVet([]string)                    {}
func runRefs([]string, io.Writer) error  { return errors.New("not built") }
func runTauri([]string, io.Writer) error { return errors.New("not built") }
