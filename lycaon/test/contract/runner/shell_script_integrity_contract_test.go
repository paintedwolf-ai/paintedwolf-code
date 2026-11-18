package contract

import (
	"bytes"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Vendored code and test inputs are not first-party executable scripts.
var shellInventoryExcludedSegments = map[string]bool{
	"testdata": true, "fixtures": true, "vendor": true, "third_party": true, "node_modules": true,
}

var shellShebang = regexp.MustCompile(`^#!\s*(?:/usr/bin/env\s+)?(?:\S*/)?(?:bash|sh)(?:\s|$)`)

// shellRepo is the checkout's file inventory and its first-party shell scripts.
type shellRepo struct {
	root    string
	files   map[string]bool
	scripts []string
}

func loadShellRepo(t *testing.T) shellRepo {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	raw, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	contractcheck.FailErr(t, "list checkout files", err)
	repo := shellRepo{root: root, files: map[string]bool{}}
	for _, rel := range strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00") {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		repo.files[rel] = true
		if slices.ContainsFunc(strings.Split(path.Dir(rel), "/"), func(s string) bool { return shellInventoryExcludedSegments[s] }) {
			continue
		}
		if isShellScript(filepath.Join(root, filepath.FromSlash(rel)), rel) {
			repo.scripts = append(repo.scripts, rel)
		}
	}
	slices.Sort(repo.scripts)
	if len(repo.scripts) == 0 {
		t.Fatal("no shell scripts found; the inventory walk is broken")
	}
	return repo
}

func isShellScript(absolute, rel string) bool {
	switch ext := path.Ext(rel); {
	case ext == ".sh" || ext == ".bash" || path.Base(rel) == ".envrc":
		return true
	case ext != "":
		return false
	}
	file, err := os.Open(absolute)
	if err != nil {
		return false
	}
	defer file.Close()
	head := make([]byte, 128)
	n, _ := file.Read(head)
	line, _, _ := bytes.Cut(head[:n], []byte("\n"))
	return shellShebang.Match(line)
}

func (r shellRepo) read(t *testing.T, rel string) string {
	t.Helper()
	return contractcheck.ReadRepoFile(t, r.root, rel)
}

func TestShellScriptsParse(t *testing.T) {
	t.Parallel()
	repo := loadShellRepo(t)
	var violations []string
	for _, rel := range repo.scripts {
		out, err := exec.CommandContext(t.Context(), "bash", "-n", filepath.Join(repo.root, filepath.FromSlash(rel))).CombinedOutput()
		if err != nil {
			violations = append(violations, rel+": "+strings.TrimSpace(strings.ReplaceAll(string(out), repo.root+string(filepath.Separator), "")))
		}
		for _, edge := range shellSourceEdges(repo, rel, repo.read(t, rel)) {
			if edge.missing {
				violations = append(violations, rel+":"+strconv.Itoa(edge.line)+": sources "+edge.raw+", which resolves to no file in the checkout")
			}
		}
	}
	contractcheck.FailViolations(t,
		"shell scripts must parse with `bash -n` and source only files that exist; fix the syntax error or the sourced path", violations)
}

// shellSourceEdge is one `source`/`.` statement and the checkout file it names.
type shellSourceEdge struct {
	line    int
	raw     string
	target  string
	missing bool
}

var (
	shellSourceCommand   = regexp.MustCompile(`(?:^|[;&|]\s*|\b(?:then|do|else)\s+)(?:source|\.)\s+`)
	shellLeadingVariable = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?(/.*)?$`)
)

// shellSourceEdges resolves each sourced script by its literal suffix, searched
// from the script's directory upward, so `${ROOT}/scripts/x.sh`,
// `$(dirname "$0")/x.sh`, and `${DIR}/e2e/x.sh` all name checkout files.
// Targets that are not scripts (session env files, `$1`) are runtime inputs.
func shellSourceEdges(repo shellRepo, rel, body string) []shellSourceEdge {
	var edges []shellSourceEdge
	for index, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, loc := range shellSourceCommand.FindAllStringIndex(trimmed, -1) {
			raw := shellWord(trimmed[loc[1]:])
			tail := strings.TrimPrefix(path.Clean("/"+shellResolvedTail(body, raw, 0)), "/")
			if ext := path.Ext(tail); ext != ".sh" && ext != ".bash" {
				continue
			}
			edge := shellSourceEdge{line: index + 1, raw: raw}
			for dir := path.Dir(rel); ; dir = path.Dir(dir) {
				if candidate := path.Join(dir, tail); repo.files[candidate] {
					edge.target = candidate
					break
				}
				if dir == "." {
					break
				}
			}
			edge.missing = edge.target == ""
			edges = append(edges, edge)
		}
	}
	return edges
}

// shellWord reads one shell word, honoring quotes and $(...) nesting.
func shellWord(s string) string {
	depth, quote := 0, byte(0)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			i++
		case quote == '"' && depth == 0:
			if c == '"' {
				quote = 0
			} else if c == '$' && i+1 < len(s) && s[i+1] == '(' {
				depth++
				i++
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			depth++
			i++
		case c == ')' && depth > 0:
			depth--
		case depth > 0:
		case c == '"' || c == '\'':
			quote = c
		case c == ' ' || c == '\t' || c == ';' || c == '&' || c == '|' || c == ')':
			return s[:i]
		}
	}
	return s
}

// shellResolvedTail is the checkout-relative literal suffix of a sourced
// word: a leading `$VAR/` expands through VAR's assignment in the script, and
// any other expansion keeps only the literal text after it.
func shellResolvedTail(body, raw string, depth int) string {
	unquoted := strings.NewReplacer(`"`, "", `'`, "").Replace(raw)
	if match := shellLeadingVariable.FindStringSubmatch(unquoted); match != nil && depth < 4 {
		if value := shellAssignment(body, match[1]); value != "" {
			return shellResolvedTail(body, value, depth+1) + match[2]
		}
		return match[2]
	}
	last := max(strings.LastIndexAny(unquoted, "})"), -1)
	if dollar := strings.LastIndex(unquoted, "$"); dollar > last {
		end := dollar + 1
		for end < len(unquoted) && isShellNameByte(unquoted[end]) {
			end++
		}
		last = end - 1
	}
	return unquoted[last+1:]
}

func shellAssignment(body, name string) string {
	assignment := regexp.MustCompile(`(?m)^\s*(?:export\s+|local\s+|readonly\s+)?` + regexp.QuoteMeta(name) + `=(.*)$`)
	if match := assignment.FindStringSubmatch(body); match != nil {
		return shellWord(match[1])
	}
	return ""
}

func isShellNameByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
