package theme

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// These bounds keep authored glyph payloads small.
const (
	maxIconBytes = 4096
	maxIconNodes = 64
	maxIconDepth = 4
)

// IconNode is one validated element from the closed vocabulary.
type IconNode struct {
	Tag string
	// Emission sorts Attrs for a stable wire form.
	Attrs map[string]string
	// Children is non-empty only for `g`.
	Children []IconNode
}

// iconElements admits only inert shapes and grouping.
var iconElements = map[string]bool{
	"path": true, "circle": true, "rect": true, "ellipse": true,
	"line": true, "polyline": true, "polygon": true, "g": true,
}

// iconPaintValues limits glyphs to inherited paint or none.
var iconPaintValues = map[string]bool{"none": true, "currentColor": true}

// transformArgCounts defines the closed transform arities.
var transformArgCounts = map[string][]int{
	"translate": {1, 2},
	"scale":     {1, 2},
	"rotate":    {1, 3},
	"skewX":     {1},
	"skewY":     {1},
	"matrix":    {6},
}

// maxTransformFunctions bounds one attribute's function list.
const maxTransformFunctions = 8

// transformCall accepts one flat `name(args)` call.
var transformCall = regexp.MustCompile(`^([A-Za-z]+)\(([^()]*)\)`)

// validateTransform returns one canonical transform spelling.
func validateTransform(element, raw string) (string, error) {
	rest := strings.TrimSpace(raw)
	if rest == "" {
		return "", fmt.Errorf("<%s transform=%q>: a transform names at least one function", element, raw)
	}
	var out []string
	for rest != "" {
		match := transformCall.FindStringSubmatch(rest)
		if match == nil {
			return "", fmt.Errorf(
				"<%s transform=%q>: a transform is a list of %s calls",
				element, raw, strings.Join(sortedTransformNames(), ", "))
		}
		name, args := match[1], strings.TrimSpace(match[2])
		counts, known := transformArgCounts[name]
		if !known {
			return "", fmt.Errorf(
				"<%s transform=%q>: %s is not one of the transforms a glyph may use (%s)",
				element, raw, name, strings.Join(sortedTransformNames(), ", "))
		}
		numbers, err := transformNumbers(element, raw, name, args)
		if err != nil {
			return "", err
		}
		if !slices.Contains(counts, len(numbers)) {
			return "", fmt.Errorf(
				"<%s transform=%q>: %s takes %s arguments, not %d",
				element, raw, name, joinCounts(counts), len(numbers))
		}
		out = append(out, name+"("+strings.Join(numbers, " ")+")")
		if len(out) > maxTransformFunctions {
			return "", fmt.Errorf(
				"<%s transform=%q>: more than %d transforms in one attribute",
				element, raw, maxTransformFunctions)
		}
		rest = strings.TrimLeft(rest[len(match[0]):], ", \t\n\r")
	}
	return strings.Join(out, " "), nil
}

func transformNumbers(element, raw, name, args string) ([]string, error) {
	if args == "" {
		return nil, nil
	}
	values, err := parseSVGNumberList(args)
	if err != nil {
		return nil, fmt.Errorf(
			"<%s transform=%q>: %s takes a well-formed list of finite numbers",
			element, raw, name)
	}
	numbers := make([]string, 0, len(values))
	for _, value := range values {
		numbers = append(numbers, strconv.FormatFloat(value, 'g', -1, 64))
	}
	return numbers, nil
}

func sortedTransformNames() []string {
	out := make([]string, 0, len(transformArgCounts))
	for name := range transformArgCounts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func joinCounts(counts []int) string {
	out := make([]string, 0, len(counts))
	for _, n := range counts {
		out = append(out, strconv.Itoa(n))
	}
	return strings.Join(out, " or ")
}

func parseIconGeometry(markup string) ([]IconNode, error) {
	if len(markup) > maxIconBytes {
		return nil, fmt.Errorf("glyph is %d bytes, over the %d-byte limit",
			len(markup), maxIconBytes)
	}
	// Decode sibling shapes without adding a synthetic root.
	dec := xml.NewDecoder(strings.NewReader(markup))
	dec.Strict = true

	var count int
	nodes, err := parseIconChildren(dec, 0, &count)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("glyph draws nothing")
	}
	return nodes, nil
}

func parseIconChildren(dec *xml.Decoder, depth int, count *int) ([]IconNode, error) {
	if depth > maxIconDepth {
		return nil, fmt.Errorf("glyph nests deeper than %d groups", maxIconDepth)
	}
	var out []IconNode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("glyph is not well-formed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			node, err := parseIconElement(dec, t, depth, count)
			if err != nil {
				return nil, err
			}
			out = append(out, node)
		case xml.EndElement:
			return out, nil
		case xml.CharData:
			// Only formatting whitespace is allowed between shapes.
			if strings.TrimSpace(string(t)) != "" {
				return nil, fmt.Errorf("glyph carries text content; a glyph is shapes only")
			}
		case xml.Comment, xml.ProcInst, xml.Directive:
			return nil, fmt.Errorf("glyph carries a %s; a glyph is shapes only", tokenKind(tok))
		}
	}
}

func parseIconElement(
	dec *xml.Decoder, start xml.StartElement, depth int, count *int,
) (IconNode, error) {
	name := start.Name.Local
	if !iconElements[name] {
		return IconNode{}, fmt.Errorf(
			"<%s> is not one of the shapes a glyph may use (%s)",
			name, strings.Join(sortedElementNames(), ", "))
	}
	*count++
	if *count > maxIconNodes {
		return IconNode{}, fmt.Errorf("glyph has more than %d shapes", maxIconNodes)
	}

	node := IconNode{Tag: name, Attrs: map[string]string{}}
	for _, attr := range start.Attr {
		key, value, err := validateIconAttr(name, attr)
		if err != nil {
			return IconNode{}, err
		}
		node.Attrs[key] = value
	}

	children, err := parseIconChildren(dec, depth+1, count)
	if err != nil {
		return IconNode{}, err
	}
	if len(children) > 0 && name != "g" {
		return IconNode{}, fmt.Errorf("<%s> cannot contain other shapes", name)
	}
	node.Children = children
	if err := validateDrawableElement(node); err != nil {
		return IconNode{}, fmt.Errorf("<%s>: %w", name, err)
	}
	return node, nil
}

func validateIconAttr(element string, attr xml.Attr) (string, string, error) {
	// Reject namespaced attributes; the vocabulary has none.
	if attr.Name.Space != "" {
		return "", "", fmt.Errorf(
			"<%s %s:%s> is namespaced; a glyph references nothing outside itself",
			element, attr.Name.Space, attr.Name.Local)
	}
	key := attr.Name.Local
	value := strings.TrimSpace(attr.Value)

	if key == "fill" || key == "stroke" {
		if !iconPaintValues[value] {
			return "", "", fmt.Errorf(
				"<%s %s=%q>: a glyph is one color — use currentColor or none, "+
					"and the theme's palette paints it",
				element, key, attr.Value)
		}
		return key, value, nil
	}
	if !iconAttrsByElement[element][key] {
		return "", "", fmt.Errorf(
			"<%s %s> is not a geometry attribute a glyph may set", element, key)
	}
	if key == "transform" {
		normalized, err := validateTransform(element, value)
		if err != nil {
			return "", "", err
		}
		return key, normalized, nil
	}
	if err := validateGeometryAttr(element, key, value); err != nil {
		return "", "", fmt.Errorf("<%s %s=%q>: %w", element, key, attr.Value, err)
	}
	return key, value, nil
}

func sortedElementNames() []string {
	out := make([]string, 0, len(iconElements))
	for name := range iconElements {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func tokenKind(tok xml.Token) string {
	switch tok.(type) {
	case xml.Comment:
		return "comment"
	case xml.ProcInst:
		return "processing instruction"
	default:
		return "directive"
	}
}
