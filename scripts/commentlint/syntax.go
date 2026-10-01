package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/gotreesitter"
)

type syntaxParser struct {
	language *gotreesitter.Language
	parser   *gotreesitter.Parser
}

func extractComments(engine syntaxParser, language string, src []byte, timeout time.Duration) ([]comment, bool, error) {
	engine.parser.SetTimeoutMicros(uint64(timeout.Microseconds()))
	return parseComments(engine, language, src)
}

func parseComments(engine syntaxParser, language string, src []byte) (comments []comment, timedOut bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			comments = nil
			timedOut = false
			err = fmt.Errorf("parser panic: %v", recovered)
		}
	}()
	tree, err := engine.parser.Parse(src)
	if err != nil {
		return nil, false, err
	}
	defer tree.Release()
	if tree.ParseStoppedEarly() {
		return nil, true, nil
	}

	var walk func(*gotreesitter.Node)
	walk = func(node *gotreesitter.Node) {
		if node == nil {
			return
		}
		kind := node.Type(engine.language)
		if text, ok := commentText(language, kind, node, src); ok {
			start := node.StartPoint()
			comments = append(comments, comment{
				Language: language,
				Kind:     kind,
				StartRow: int(start.Row) + 1,
				StartCol: int(start.Column) + 1,
				Text:     text,
			})
			return
		}
		for i := 0; i < node.ChildCount(); i++ {
			walk(node.Child(i))
		}
	}
	walk(tree.RootNode())
	return comments, false, nil
}

func commentText(language, kind string, node *gotreesitter.Node, src []byte) (string, bool) {
	if strings.Contains(strings.ToLower(kind), "comment") {
		return node.Text(src), true
	}
	if language != "html" && language != "markdown" && language != "xml" {
		return "", false
	}
	text := node.Text(src)
	trimmed := strings.TrimSpace(text)
	return text, strings.HasPrefix(trimmed, "<!--") && strings.HasSuffix(trimmed, "-->")
}
