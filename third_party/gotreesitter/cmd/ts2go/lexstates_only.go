package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// RunLexStatesOnlyManifest clones each requested grammar repo in the manifest
// and (re)generates only its *_external_lex_states_gen.go sidecar file. It
// never touches grammar_blobs/*.bin, *_register.go, or
// embedded_grammars_gen.go, so it is safe to run against grammars whose
// embedded blob and registration were produced by a different pipeline (e.g.
// hand-written external scanners registered via zzz_scanner_attachments.go).
//
// If only is non-empty, generation is restricted to the named languages
// (matching ManifestEntry.Name exactly).
func RunLexStatesOnlyManifest(manifestPath, outDir, pkg string, only []string) error {
	entries, err := ParseManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("manifest is empty: %s", manifestPath)
	}

	wanted := map[string]bool{}
	for _, n := range only {
		n = strings.TrimSpace(n)
		if n != "" {
			wanted[n] = true
		}
	}
	if len(wanted) > 0 {
		filtered := entries[:0]
		for _, e := range entries {
			if wanted[e.Name] {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	if len(entries) == 0 {
		return fmt.Errorf("no manifest entries matched -only filter")
	}

	tmpRoot, err := os.MkdirTemp("", "ts2go-lexstates-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmpRoot)

	grp, grpCtx := errgroup.WithContext(context.Background())
	grp.SetLimit(runtime.GOMAXPROCS(-1))
	var mu sync.Mutex
	var written []string
	var skippedNoStates []string

	for _, entry := range entries {
		entry := entry
		if err := grpCtx.Err(); err != nil {
			break
		}
		grp.Go(func() error {
			if err := grpCtx.Err(); err != nil {
				return err
			}
			repoDir := filepath.Join(tmpRoot, safeFileBase(entry.Name))
			if err := cloneRepo(entry.RepoURL, entry.Commit, repoDir); err != nil {
				return fmt.Errorf("%s: clone: %w", entry.Name, err)
			}

			parserPath := filepath.Join(repoDir, entry.Subdir, "parser.c")
			if _, err := os.Stat(parserPath); err != nil {
				// findParserC's fallback walk is subdir-agnostic and prefers
				// any .../src/parser.c it finds first; in a multi-grammar
				// repo (several parser.c files under different subdirs) it
				// can pick the wrong one. The entry.Subdir path above is
				// what makes selection safe today — this fallback only
				// fires when that exact path is missing.
				detected, derr := findParserC(repoDir)
				if derr != nil {
					return fmt.Errorf("%s: parser.c not found under %s", entry.Name, repoDir)
				}
				parserPath = detected
			}
			source, err := os.ReadFile(parserPath)
			if err != nil {
				return fmt.Errorf("%s: read %s: %w", entry.Name, parserPath, err)
			}

			grammar, err := ExtractGrammar(string(source))
			if err != nil {
				return fmt.Errorf("%s: extract: %w", entry.Name, err)
			}
			grammar.Name = entry.Name

			if len(grammar.ExternalLexStates) == 0 {
				mu.Lock()
				skippedNoStates = append(skippedNoStates, entry.Name)
				mu.Unlock()
				fmt.Printf("%s: no ts_external_scanner_states table found; skipping\n", entry.Name)
				return nil
			}

			sourceComment := externalLexStatesSourceComment(entry)
			if err := writeExternalLexStatesSidecar(outDir, pkg, entry.Name, sourceComment, grammar.ExternalLexStates); err != nil {
				return fmt.Errorf("%s: write external lex states: %w", entry.Name, err)
			}

			mu.Lock()
			written = append(written, entry.Name)
			mu.Unlock()
			fmt.Printf("generated %s_external_lex_states_gen.go (%d states, %d external tokens)\n",
				safeFileBase(entry.Name), len(grammar.ExternalLexStates), grammar.ExternalTokenCount)
			return nil
		})
	}
	if err := grp.Wait(); err != nil {
		return err
	}

	fmt.Printf("lexstates-only: wrote %d file(s), skipped %d (no external lex states)\n", len(written), len(skippedNoStates))
	return nil
}
