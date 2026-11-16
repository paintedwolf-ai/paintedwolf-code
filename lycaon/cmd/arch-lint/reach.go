package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/archlint/reach"
)

func runReach(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reach", flag.ContinueOnError)
	dir := fs.String("dir", ".", "module directory")
	goos := fs.String("goos", "darwin,linux", "comma-separated target GOOS")
	tags := fs.String("tags", "", "comma-separated build tags")
	report := fs.String("report", "", "report only package paths matching this pattern")
	skip := fs.String("skip", "", "comma-separated patterns for test-support package paths")
	schema := fs.String("schema", "", "schema file whose tables need a reader")
	scope := fs.String("scope", "", "comma-separated package patterns judged even when no root imports them")
	product := fs.String("product", "", "comma-separated main packages that ship to users")
	emitters := fs.String("emitters", "", "JSON emitter spec judging declared codes, facts and anchors")
	repo := fs.String("repo", "..", "repository root the emitter spec is relative to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := reach.Config{
		Dir: *dir, Patterns: fs.Args(), Scope: splitList(*scope), GOOS: splitList(*goos), Tags: splitList(*tags), Schema: *schema,
		Product: splitList(*product),
	}
	if *report != "" {
		re, err := regexp.Compile(*report)
		if err != nil {
			return err
		}
		cfg.Report = re
	}
	for _, p := range splitList(*skip) {
		re, err := regexp.Compile(p)
		if err != nil {
			return err
		}
		cfg.Skip = append(cfg.Skip, re)
	}
	if *emitters != "" {
		var spec reach.EmitterSpec
		if err := json.Unmarshal([]byte(*emitters), &spec); err != nil {
			return fmt.Errorf("emitters: %w", err)
		}
		loaded, err := reach.LoadEmitters(*repo, spec)
		if err != nil {
			return err
		}
		cfg.Emitters = loaded
	}
	findings, err := reach.Run(cfg)
	if err != nil {
		return err
	}
	return writeJSON(out, findings)
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
