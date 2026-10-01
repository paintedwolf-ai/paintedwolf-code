package sourceview

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

type htmlScript struct {
	info       scriptInfo
	start, end int
}

// Analysis-only attributes connect the HTML tree to original tag offsets. They
// contain no delimiters that could change the input's tokenization boundaries.
func markHTMLScripts(source []byte) ([]byte, string) {
	lower := asciiLower(string(source))
	marker := "data-paintedwolf-source"
	for strings.Contains(lower, marker) {
		marker += "x"
	}
	var marked bytes.Buffer
	previous := 0
	for offset := 0; offset+7 < len(source); offset++ {
		if source[offset] != '<' || lower[offset+1:offset+7] != "script" {
			continue
		}
		switch source[offset+7] {
		case ' ', '\t', '\n', '\r', '\f', '/', '>':
		default:
			continue
		}
		marked.Write(source[previous : offset+7])
		fmt.Fprintf(&marked, " %s=%d ", marker, offset)
		previous = offset + 7
	}
	marked.Write(source[previous:])
	return marked.Bytes(), marker
}

func executableHTMLScripts(source []byte) ([]htmlScript, []Limitation, error) {
	marked, marker := markHTMLScripts(source)
	document, err := html.Parse(bytes.NewReader(marked))
	if err != nil {
		return nil, nil, err
	}
	var scripts []htmlScript
	var limitations []Limitation
	originalLines := lineOffsets(source)
	var walk func(*html.Node, bool) error
	walk = func(node *html.Node, inert bool) error {
		inert = inert || node.Type == html.ElementNode && node.Namespace == "" && node.Data == "template"
		if node.Type == html.ElementNode && node.Data == "script" && !inert {
			if node.Namespace != "" {
				offset, err := htmlScriptOffset(node, marker)
				if err != nil {
					return err
				}
				limitations = append(limitations, Limitation{Construct: "embedded_namespace",
					Detail: "Embedded script in a foreign HTML namespace requires namespace-aware projection",
					Start:  sourcePosition(offset, originalLines)})
				return nil
			}
			info := classifyHTMLScript(node.Attr)
			if info.mode != dataScript {
				offset, err := htmlScriptOffset(node, marker)
				if err != nil {
					return err
				}
				script, err := readHTMLScript(source, offset, info)
				if err != nil {
					return err
				}
				scripts = append(scripts, script)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child, inert); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(document, false); err != nil {
		return nil, nil, err
	}
	// HTML tree recovery can move nodes; classic scripts execute in parser order.
	sort.Slice(scripts, func(i, j int) bool { return scripts[i].start < scripts[j].start })
	return scripts, limitations, nil
}

func htmlScriptOffset(node *html.Node, marker string) (int, error) {
	for _, attr := range node.Attr {
		if attr.Key == marker {
			offset, err := strconv.Atoi(attr.Val)
			if err == nil {
				return offset, nil
			}
		}
	}
	return 0, &Limitation{Construct: "embedded_script", Detail: "Parsed HTML script has no source location"}
}

func readHTMLScript(source []byte, offset int, info scriptInfo) (htmlScript, error) {
	tokenizer := html.NewTokenizer(bytes.NewReader(source[offset:]))
	tokenizer.Next()
	start := offset + len(tokenizer.Raw())
	position := start
	for {
		kind := tokenizer.Next()
		raw := tokenizer.Raw()
		if kind == html.EndTagToken && tokenizer.Token().Data == "script" {
			return htmlScript{info: info, start: start, end: position}, nil
		}
		if kind == html.ErrorToken {
			if !errors.Is(tokenizer.Err(), io.EOF) {
				return htmlScript{}, tokenizer.Err()
			}
			return htmlScript{}, &Limitation{Construct: "embedded_script", Detail: "Executable script has no closing tag"}
		}
		position += len(raw)
	}
}
