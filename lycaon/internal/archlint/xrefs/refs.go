// Package xrefs checks references that cross languages: commands and scripts
// something runs, and Tauri commands Den invokes and capabilities permit.
package xrefs

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/archlint"
)

// Finding kinds.
const (
	KindUnreferencedCommand = "unreferenced-command"
	KindUnreferencedScript  = "unreferenced-script"
)

// trackedFile is one file git tracks and whether it is executable.
type trackedFile struct {
	path       string
	executable bool
}

func tracked(root string) ([]trackedFile, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "-s", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []trackedFile
	for _, entry := range strings.Split(string(out), "\x00") {
		meta, p, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		files = append(files, trackedFile{path: p, executable: strings.HasPrefix(meta, "100755")})
	}
	return files, nil
}

var (
	cmdToken    = regexp.MustCompile(`cmd/([A-Za-z0-9_-]+)`)
	wordToken   = regexp.MustCompile(`[A-Za-z0-9_.-]+`)
	pythonToken = regexp.MustCompile(`(?m)^\s*(?:import|from)\s+([A-Za-z0-9_]+)`)
)

// subject is one command or script, the tokens that name it, and the files
// that do not count as referencing it: its own and its tests'.
type subject struct {
	kind, path string
	tokens     []string
	own        func(string) bool
}

// Refs reports every lycaon/cmd binary and every executable under scripts/
// that no other tracked file names.
func Refs(root string) ([]archlint.Finding, error) {
	files, err := tracked(root)
	if err != nil {
		return nil, err
	}
	var subjects []subject
	seenCmd := map[string]bool{}
	for _, f := range files {
		if rest, ok := strings.CutPrefix(f.path, "lycaon/cmd/"); ok {
			name, _, nested := strings.Cut(rest, "/")
			if !nested || seenCmd[name] {
				continue
			}
			seenCmd[name] = true
			dir := "lycaon/cmd/" + name + "/"
			subjects = append(subjects, subject{
				kind: KindUnreferencedCommand, path: "lycaon/cmd/" + name, tokens: []string{"cmd/" + name},
				own: func(p string) bool { return strings.HasPrefix(p, dir) },
			})
		}
		if strings.HasPrefix(f.path, "scripts/") && f.executable {
			subjects = append(subjects, scriptSubject(f.path))
		}
	}
	referenced := make([]bool, len(subjects))
	for _, f := range files {
		raw, err := os.ReadFile(path.Join(root, f.path))
		if err != nil || bytes.IndexByte(raw, 0) >= 0 {
			continue
		}
		tokens := map[string]bool{}
		for _, m := range cmdToken.FindAllSubmatch(raw, -1) {
			tokens["cmd/"+string(m[1])] = true
		}
		for _, w := range wordToken.FindAll(raw, -1) {
			tokens[string(w)] = true
		}
		for _, m := range pythonToken.FindAllSubmatch(raw, -1) {
			tokens["import "+string(m[1])] = true
		}
		for i, s := range subjects {
			if referenced[i] || s.own(f.path) {
				continue
			}
			for _, t := range s.tokens {
				if tokens[t] {
					referenced[i] = true
					break
				}
			}
		}
	}
	var out []archlint.Finding
	for i, s := range subjects {
		if !referenced[i] {
			out = append(out, archlint.Finding{
				Kind: s.kind, Key: s.path, Pos: s.path,
				Message: s.path + " is not run, built or named by any other file",
			})
		}
	}
	return out, nil
}

func scriptSubject(p string) subject {
	base := path.Base(p)
	stem := strings.TrimSuffix(base, path.Ext(base))
	tokens := []string{base}
	if path.Ext(base) == ".py" {
		tokens = append(tokens, "import "+stem)
	}
	dir := path.Dir(p)
	return subject{
		kind: KindUnreferencedScript, path: p, tokens: tokens,
		own: func(other string) bool {
			if other == p {
				return true
			}
			name := path.Base(other)
			return path.Dir(other) == dir && (strings.HasPrefix(name, "test_"+stem) || strings.HasPrefix(name, stem+"_test"))
		},
	}
}
