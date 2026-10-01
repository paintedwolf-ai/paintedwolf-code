package project

import (
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/sourceloc"
)

// SourceQuery is a quick-open query read as a path portion and an optional location.
type SourceQuery struct {
	// Path is lowercase and slash-separated; it may span a root folder's own path.
	Path string
	// Absolute keeps an absolute query's own spelling for file system checks.
	Absolute string
	// Remote names a file by its code host repository instead of a local path.
	Remote                *SourceRemoteFile
	Line, Column, EndLine int
}

// SourceRemoteFile is a code host file link: the repository, then ref and path segments whose
// boundary only the local checkout can settle, since refs may contain slashes.
type SourceRemoteFile struct {
	// Repo is the lowercase host and repository path, as SourceRemoteRepo normalizes remotes.
	Repo     string
	Segments []string
}

// SourcePathStyle is the host convention pasted paths follow.
type SourcePathStyle struct {
	// Windows paths use drive letters, UNC shares, and either separator; `\` never escapes.
	Windows bool
	// Home expands a leading `~`.
	Home string
}

// HostSourcePathStyle describes this host.
func HostSourcePathStyle() SourcePathStyle {
	home, _ := os.UserHomeDir()
	return SourcePathStyle{Windows: runtime.GOOS == "windows", Home: home}
}

// Tracebacks that name the file and line in prose: `File "a.py", line 42`.
var pythonTraceLocation = regexp.MustCompile(`File "([^"]+)", line (\d+)`)

// ParseSourceQuery reads pasted and typed file locations, including a location inside a pasted
// compiler, test, or stack trace line.
func ParseSourceQuery(raw string, style SourcePathStyle) SourceQuery {
	var out SourceQuery
	text := unwrapSourceQuery(strings.TrimSpace(raw))
	if match := pythonTraceLocation.FindStringSubmatch(text); match != nil {
		text = match[1]
		out.Line, _ = strconv.Atoi(match[2])
	} else if !strings.ContainsAny(text, " \t\n") {
		text = strings.TrimRight(text, ":,;")
		text, out.Line, out.Column, out.EndLine = cutSourceQueryLocation(text)
	} else if token, line, column, end, ok := traceLocation(text); ok {
		text, out.Line, out.Column, out.EndLine = token, line, column, end
	}
	text = style.readURL(text, &out)
	if out.Remote != nil {
		return out
	}
	if !style.Windows {
		text = unescapeShellPath(text)
	}
	native := style.slashes(text)
	if style.Home != "" && (native == "~" || strings.HasPrefix(native, "~/")) {
		native = style.slashes(style.Home) + native[1:]
	}
	for strings.HasPrefix(native, "./") {
		native = native[2:]
	}
	out.Path = strings.ToLower(native)
	if style.absolute(native) {
		out.Absolute = native
	}
	return out
}

// traceLocation finds the first whitespace-separated token that ends in a location. A bare file
// name joins a directory token printed just before it.
func traceLocation(text string) (string, int, int, int, bool) {
	tokens := strings.Fields(text)
	for i, token := range tokens {
		path, line, column, end, ok := tokenLocation(token)
		if !ok {
			continue
		}
		if i > 0 && !strings.ContainsAny(path, `/\`) {
			if dir := trimTraceToken(tokens[i-1]); strings.Contains(dir, "/") && !strings.ContainsAny(dir, ":=") {
				path = strings.TrimSuffix(dir, "/") + "/" + path
			}
		}
		return path, line, column, end, true
	}
	return "", 0, 0, 0, false
}

func tokenLocation(token string) (string, int, int, int, bool) {
	for _, candidate := range []string{strings.TrimRight(token, ":,;."), trimTraceToken(token)} {
		if path, line, column, end := cutSourceQueryLocation(candidate); line > 0 {
			return path, line, column, end, true
		}
	}
	return "", 0, 0, 0, false
}

// trimTraceToken drops the brackets, quotes, and punctuation traces wrap locations in.
func trimTraceToken(token string) string {
	return strings.TrimRight(strings.TrimLeft(token, "([{<\"'`"), ")]}>\"'`:,;.")
}

// readURL turns a file, code host, or development server URL into the path it names.
func (s SourcePathStyle) readURL(text string, out *SourceQuery) string {
	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lower, "file://"):
		return fileURLPath(text, s)
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		u, err := url.Parse(text)
		if err != nil {
			return text
		}
		if remote := codeHostFile(u); remote != nil {
			out.Remote = remote
			return ""
		}
		// Development servers serve files outside their root under /@fs/.
		if abs, ok := strings.CutPrefix(u.Path, "/@fs/"); ok {
			return "/" + abs
		}
		return u.Path
	default:
		// Bundler schemes such as `name:///./src/a.js` wrap a project-relative path.
		if scheme, rest, ok := strings.Cut(text, "://"); ok && !strings.ContainsAny(scheme, `/\`) && len(scheme) > 1 {
			return strings.TrimLeft(rest, "/")
		}
		return text
	}
}

// codeHostFile reads code host file links: /owner/repo/{blob,tree,blame,raw,src}/ref/path.
func codeHostFile(u *url.URL) *SourceRemoteFile {
	host := strings.ToLower(u.Hostname())
	segments := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	remote := func(repo []string, rest []string) *SourceRemoteFile {
		if len(repo) < 2 || len(rest) < 2 {
			return nil
		}
		return &SourceRemoteFile{Repo: host + "/" + strings.ToLower(strings.TrimSuffix(strings.Join(repo, "/"), ".git")), Segments: rest}
	}
	if host == "raw.githubusercontent.com" && len(segments) >= 4 {
		return &SourceRemoteFile{Repo: "github.com/" + strings.ToLower(segments[0]+"/"+segments[1]), Segments: segments[2:]}
	}
	// A /-/ segment ends a project path that may nest groups.
	for i, segment := range segments {
		if segment == "-" && i+1 < len(segments) && isCodeHostView(segments[i+1]) {
			return remote(segments[:i], segments[i+2:])
		}
	}
	if len(segments) < 4 {
		return nil
	}
	switch view := segments[2]; {
	case isCodeHostView(view):
		return remote(segments[:2], segments[3:])
	case view == "src" && len(segments) > 4 && (segments[3] == "branch" || segments[3] == "commit" || segments[3] == "tag"):
		return remote(segments[:2], segments[4:])
	case view == "src":
		return remote(segments[:2], segments[3:])
	}
	return nil
}

func isCodeHostView(segment string) bool {
	switch segment {
	case "blob", "tree", "blame", "raw":
		return true
	}
	return false
}

// SourceRemoteRepo normalizes a git remote URL to the lowercase host and repository path code
// host links use, so ssh and https spellings of one repository compare equal.
func SourceRemoteRepo(remote string) string {
	remote = strings.TrimSpace(remote)
	var host, path string
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else if at, rest, ok := strings.Cut(remote, "@"); ok && !strings.Contains(at, "/") {
		// Short ssh form: user@host:owner/repo.git
		host, path, _ = strings.Cut(rest, ":")
	} else {
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return ""
	}
	return strings.ToLower(host + "/" + path)
}

// slashes folds a path to the slash-separated form queries compare in, keeping its case.
func (s SourcePathStyle) slashes(path string) string {
	if !s.Windows {
		return path
	}
	path = strings.ReplaceAll(path, `\`, "/")
	// Extended-length prefixes name the same file as the plain path.
	if len(path) >= 8 && strings.EqualFold(path[:8], "//?/unc/") {
		return "//" + path[8:]
	}
	return strings.TrimPrefix(path, "//?/")
}

// normalize folds a path to the lowercase, slash-separated form queries compare in.
func (s SourcePathStyle) normalize(path string) string {
	return strings.ToLower(s.slashes(path))
}

func (s SourcePathStyle) absolute(path string) bool {
	if s.Windows {
		return strings.HasPrefix(path, "//") || (len(path) >= 3 && path[1] == ':' && path[2] == '/')
	}
	return strings.HasPrefix(path, "/")
}

func fileURLPath(text string, style SourcePathStyle) string {
	u, err := url.Parse(text)
	if err != nil || u.Path == "" {
		return text
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return "//" + u.Host + u.Path
	}
	// A Windows file URL puts the drive letter after an empty host.
	if style.Windows && len(u.Path) > 2 && u.Path[0] == '/' && u.Path[2] == ':' {
		return u.Path[1:]
	}
	return u.Path
}

func unwrapSourceQuery(text string) string {
	for len(text) >= 2 {
		first, last := text[0], text[len(text)-1]
		if first != last || !strings.ContainsRune("\"'`", rune(first)) {
			break
		}
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	return text
}

func cutSourceQueryLocation(text string) (string, int, int, int) {
	path, loc, _ := sourceloc.Cut(text)
	return path, loc.Line, loc.Column, loc.EndLine
}

// unescapeShellPath reverses the backslash escapes terminals add to dragged or copied paths.
func unescapeShellPath(text string) string {
	if !strings.Contains(text, `\`) {
		return text
	}
	var out strings.Builder
	escaped := false
	for _, char := range text {
		if char == '\\' && !escaped {
			escaped = true
			continue
		}
		escaped = false
		out.WriteRune(char)
	}
	return out.String()
}
