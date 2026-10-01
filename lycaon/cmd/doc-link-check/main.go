// Command doc-link-check validates documentation links and anchors.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	linkRe     = regexp.MustCompile(`\]\(([^()\s]+)(?:\s+"[^"]*")?\)`)
	headingRe  = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*$`)
	explicitRe = regexp.MustCompile(`\s*\{#([A-Za-z0-9_-]+)\}\s*$`)
	htmlAnchor = regexp.MustCompile(`<a\s+(?:id|name)="([A-Za-z0-9_-]+)"`)
	inlineCode = regexp.MustCompile("`[^`]*`")
)

func main() {
	root := flag.String("root", "", "repository root (defaults to cwd)")
	flag.Parse()
	if *root == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		*root = wd
	}

	docsDir := filepath.Join(*root, "docs")
	var files []string
	err := filepath.WalkDir(docsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	sort.Strings(files)

	anchors := map[string]map[string]bool{}
	var problems []string
	for _, file := range files {
		anchors[file] = fileAnchors(file)
	}
	for _, file := range files {
		problems = append(problems, checkFile(file, anchors)...)
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		fmt.Fprintf(os.Stderr, "doc-link-check: %d broken link(s)\n", len(problems))
		os.Exit(1)
	}
}

// prunedLines returns the file's lines with fenced code blocks blanked so link
// and heading syntax inside examples never registers.
func prunedLines(path string) []string {
	raw, err := os.ReadFile(path) // #nosec G304 -- repo-relative doc path from the walked tree
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	lines := strings.Split(string(raw), "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			lines[i] = ""
			continue
		}
		if inFence {
			lines[i] = ""
		}
	}
	return lines
}

func fileAnchors(path string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	for _, line := range prunedLines(path) {
		// A section reachable by link without being its own heading carries an
		// explicit HTML anchor instead.
		for _, a := range htmlAnchor.FindAllStringSubmatch(line, -1) {
			out[a[1]] = true
		}
		m := headingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := m[1]
		if em := explicitRe.FindStringSubmatch(text); em != nil {
			out[em[1]] = true
			continue
		}
		slug := githubSlug(text)
		if n := seen[slug]; n > 0 {
			out[fmt.Sprintf("%s-%d", slug, n)] = true
		} else {
			out[slug] = true
		}
		seen[slug]++
	}
	return out
}

// githubSlug derives a lowercase heading anchor.
func githubSlug(text string) string {
	text = strings.ToLower(strings.ReplaceAll(text, "`", ""))
	var b strings.Builder
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

func checkFile(file string, anchors map[string]map[string]bool) []string {
	var problems []string
	dir := filepath.Dir(file)
	for i, line := range prunedLines(file) {
		stripped := inlineCode.ReplaceAllString(line, "")
		for _, m := range linkRe.FindAllStringSubmatch(stripped, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			path, fragment, _ := strings.Cut(target, "#")
			where := fmt.Sprintf("%s:%d", file, i+1)
			resolved := file
			if path != "" {
				resolved = filepath.Clean(filepath.Join(dir, path))
				if _, err := os.Stat(resolved); err != nil {
					problems = append(problems, fmt.Sprintf("%s: target does not exist: %s", where, target))
					continue
				}
			}
			if fragment == "" {
				continue
			}
			set, indexed := anchors[resolved]
			if !indexed {
				if !strings.HasSuffix(resolved, ".md") {
					continue
				}
				set = fileAnchors(resolved)
				anchors[resolved] = set
			}
			if !set[fragment] {
				problems = append(problems, fmt.Sprintf("%s: no heading for anchor: %s", where, target))
			}
		}
	}
	return problems
}
