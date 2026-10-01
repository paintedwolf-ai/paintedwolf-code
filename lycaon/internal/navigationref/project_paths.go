// Package navigationref parses navigation requests without filesystem work.
package navigationref

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

var pathTokenRE = regexp.MustCompile(`[-\p{L}\p{N}_@./\\]+(?::[0-9]+(?:-[0-9]+)?)?(?:#L?[0-9]+(?:-L?[0-9]+)?)?`)

// BuildProjectPathReferences binds mentions to supplied context without checking availability.
func BuildProjectPathReferences(p *project.Project, prose string, context *api.SourceContext) []api.NavigationReference {
	if p == nil || p.ID == "" {
		return []api.NavigationReference{}
	}
	source := []byte(prose)
	doc := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(source))
	found := []api.NavigationReference{}
	index := indexContext(context)
	add := func(raw, syntax string, explicit, standalone bool) {
		if len(found) >= api.MaxMessageNavigationRefs {
			return
		}
		if !explicit && !strings.ContainsAny(raw, "./\\:@") && (syntax == "text" || len(index[raw]) == 0) {
			return
		}
		ref, ok := parseReference(p, raw, explicit, standalone)
		if !ok {
			return
		}
		if !explicit {
			if bound, ok := index.bind(ref); ok {
				ref = bound
			} else if ref.RootID == "" {
				return
			}
		}
		ref.ID, ref.Syntax = "ref-"+strconv.Itoa(len(found)), syntax
		found = append(found, ref)
	}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.Link:
			add(string(node.Destination), "link", true, true)
			return ast.WalkSkipChildren, nil
		case *ast.Image, *ast.AutoLink, *ast.RawHTML, *ast.HTMLBlock:
			return ast.WalkSkipChildren, nil
		case *ast.CodeSpan:
			add(codeSpanText(node, source), "code", false, true)
			return ast.WalkSkipChildren, nil
		case *ast.FencedCodeBlock:
			if node.Language(source) == nil {
				raw := strings.TrimSpace(string(node.Lines().Value(source)))
				if !strings.ContainsAny(raw, "\r\n") {
					add(raw, "fence", false, true)
				}
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			for _, token := range pathTokenRE.FindAllString(string(node.Segment.Value(source)), -1) {
				add(strings.TrimRight(token, ".,"), "text", false, false)
			}
		}
		return ast.WalkContinue, nil
	})
	return found
}

func codeSpanText(node *ast.CodeSpan, source []byte) string {
	var value strings.Builder
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		segment := child.(*ast.Text)
		value.Write(segment.Value(source))
		if segment.SoftLineBreak() {
			value.WriteByte('\n')
		}
	}
	return value.String()
}

func parseReference(p *project.Project, raw string, explicit, standalone bool) (api.NavigationReference, bool) {
	ref := api.NavigationReference{Mention: raw, ProjectID: p.ID, Status: api.NavigationPending, Explicit: explicit}
	// A qualified form binds to the primary root when nothing names one.
	anchored := explicit
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return ref, false
	}
	parsed, parseErr := url.Parse(value)
	urlLocation := false
	if parseErr == nil && parsed.Scheme == "source" {
		if parsed.Host == "" || parsed.User != nil {
			return ref, false
		}
		query, queryErr := url.ParseQuery(parsed.RawQuery)
		if queryErr != nil || len(query) > 1 || (len(query) == 1 && (len(query["job_id"]) != 1 || query.Get("job_id") == "")) {
			return ref, false
		}
		ref.WorkerID = query.Get("job_id")
		ref.RootID = parsed.Host
		anchored = true
		value = strings.TrimPrefix(parsed.EscapedPath(), "/")
		urlLocation = true
		explicit = true
	} else if parseErr == nil && parsed.Scheme == "file" && (parsed.Host == "" || egress.SyntacticLoopback(parsed.Host)) {
		value = parsed.EscapedPath()
		urlLocation = true
		explicit = true
	} else if parseErr == nil && (parsed.Scheme != "" || parsed.Host != "") {
		if _, hasLine := sourceref.ParseLineAddress(value); !hasLine || strings.Contains(value, "://") {
			return ref, false
		}
	}
	lineValue := value
	if urlLocation {
		lineValue = "#" + parsed.Fragment
	}
	if address, ok := sourceref.ParseLineAddress(lineValue); ok {
		ref.Line, ref.EndLine = address.Line, address.EndLine
		if !urlLocation {
			value = address.Path
		}
	}
	if explicit {
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return ref, false
		}
		value = decoded
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if filepath.IsAbs(value) {
		matched := false
		for _, root := range p.Roots {
			rel, err := filepath.Rel(root.Path, value)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				if matched && (ref.RootID != root.ID || ref.Path != filepath.ToSlash(rel)) {
					return ref, false
				}
				ref.RootID = root.ID
				anchored = true
				ref.Path = filepath.ToSlash(rel)
				matched = true
			}
		}
		if !matched {
			return ref, false
		}
		value = ref.Path
	}
	if !explicit && !standalone && !strings.ContainsAny(value, "./") {
		return ref, false
	}
	if strings.HasPrefix(value, "@") && ref.RootID == "" {
		label, rel, ok := strings.Cut(value[1:], "/")
		if !ok {
			return ref, false
		}
		for _, root := range p.Roots {
			if root.Label == label {
				if ref.RootID != "" && ref.RootID != root.ID {
					return ref, false
				}
				ref.RootID = root.ID
			}
		}
		if ref.RootID == "" {
			ref.Status = api.NavigationUnavailable
		}
		value = rel
		anchored = true
	}
	if strings.HasPrefix(value, "./") || strings.HasSuffix(value, "/") {
		anchored = true
	}
	for strings.HasPrefix(value, "./") {
		value = strings.TrimPrefix(value, "./")
	}
	value = strings.TrimSuffix(value, "/")
	if strings.Contains(value, "/") {
		anchored = true
	}
	if strings.HasPrefix(value, "~") || !sourceref.ValidNavigationPath(value) {
		return ref, false
	}
	ref.Path = value
	if anchored && ref.RootID == "" && ref.Status != api.NavigationUnavailable {
		for _, root := range p.Roots {
			if root.IsPrimary {
				ref.RootID = root.ID
				break
			}
		}
	}
	return ref, true
}
