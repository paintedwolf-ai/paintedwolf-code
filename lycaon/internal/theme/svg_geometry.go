package theme

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var svgNumber = regexp.MustCompile(`^[+\-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+\-]?[0-9]+)?$`)
var svgNumberPrefix = regexp.MustCompile(`^[+\-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+\-]?[0-9]+)?`)

var commonIconAttrs = []string{
	"transform", "opacity", "fill-rule", "clip-rule", "stroke-width",
	"stroke-linecap", "stroke-linejoin", "stroke-dasharray", "stroke-dashoffset",
}

var iconAttrsByElement = map[string]map[string]bool{
	"path":     iconAttrSet("d"),
	"circle":   iconAttrSet("cx", "cy", "r"),
	"rect":     iconAttrSet("x", "y", "width", "height", "rx", "ry"),
	"ellipse":  iconAttrSet("cx", "cy", "rx", "ry"),
	"line":     iconAttrSet("x1", "y1", "x2", "y2"),
	"polyline": iconAttrSet("points"),
	"polygon":  iconAttrSet("points"),
	"g":        iconAttrSet(),
}

func iconAttrSet(specific ...string) map[string]bool {
	out := make(map[string]bool, len(specific)+len(commonIconAttrs))
	for _, key := range commonIconAttrs {
		out[key] = true
	}
	for _, key := range specific {
		out[key] = true
	}
	return out
}

func validateGeometryAttr(element, key, raw string) error {
	switch key {
	case "d":
		return validateSVGPath(raw)
	case "points":
		_, err := parseSVGPoints(raw)
		return err
	case "fill-rule", "clip-rule":
		if raw != "nonzero" && raw != "evenodd" {
			return fmt.Errorf("must be nonzero or evenodd")
		}
		return nil
	case "stroke-linecap":
		if raw != "butt" && raw != "round" && raw != "square" {
			return fmt.Errorf("must be butt, round, or square")
		}
		return nil
	case "stroke-linejoin":
		if raw != "miter" && raw != "round" && raw != "bevel" {
			return fmt.Errorf("must be miter, round, or bevel")
		}
		return nil
	case "stroke-dasharray":
		if raw == "none" {
			return nil
		}
		values, err := parseSVGNumberList(raw)
		if err != nil || len(values) == 0 {
			return fmt.Errorf("must be none or a list of non-negative numbers")
		}
		for _, value := range values {
			if value < 0 {
				return fmt.Errorf("must contain only non-negative numbers")
			}
		}
		return nil
	case "opacity":
		value, err := parseSVGNumber(raw)
		if err != nil || value <= 0 || value > 1 {
			return fmt.Errorf("must be greater than 0 and at most 1")
		}
		return nil
	case "stroke-width":
		value, err := parseSVGNumber(raw)
		if err != nil || value <= 0 {
			return fmt.Errorf("must be a positive finite number")
		}
		return nil
	case "r", "rx", "ry", "width", "height":
		value, err := parseSVGNumber(raw)
		if err != nil || value < 0 {
			return fmt.Errorf("must be a non-negative finite number")
		}
		return nil
	default:
		if _, err := parseSVGNumber(raw); err != nil {
			return fmt.Errorf("must be a finite number")
		}
		return nil
	}
}

func validateDrawableElement(node IconNode) error {
	attrs := node.Attrs
	if attrs["fill"] == "none" && attrs["stroke"] == "none" {
		return fmt.Errorf("sets both fill and stroke to none")
	}
	switch node.Tag {
	case "path":
		if attrs["d"] == "" {
			return fmt.Errorf("requires a drawing path in d")
		}
	case "circle":
		if !positiveSVGAttr(attrs, "r") {
			return fmt.Errorf("requires r greater than 0")
		}
	case "ellipse":
		if !positiveSVGAttr(attrs, "rx") || !positiveSVGAttr(attrs, "ry") {
			return fmt.Errorf("requires rx and ry greater than 0")
		}
	case "rect":
		if !positiveSVGAttr(attrs, "width") || !positiveSVGAttr(attrs, "height") {
			return fmt.Errorf("requires width and height greater than 0")
		}
	case "line":
		x1, _ := optionalSVGNumber(attrs["x1"])
		y1, _ := optionalSVGNumber(attrs["y1"])
		x2, _ := optionalSVGNumber(attrs["x2"])
		y2, _ := optionalSVGNumber(attrs["y2"])
		if x1 == x2 && y1 == y2 {
			return fmt.Errorf("requires two distinct endpoints")
		}
	case "polyline", "polygon":
		points, _ := parseSVGPoints(attrs["points"])
		minimum := 2
		if node.Tag == "polygon" {
			minimum = 3
		}
		if len(points) < minimum {
			return fmt.Errorf("requires at least %d points", minimum)
		}
		if !pointsContainDistinctPair(points) {
			return fmt.Errorf("requires distinct points")
		}
	case "g":
		if len(node.Children) == 0 {
			return fmt.Errorf("requires at least one child shape")
		}
	}
	return nil
}

func parseSVGNumber(raw string) (float64, error) {
	if !svgNumber.MatchString(raw) {
		return 0, fmt.Errorf("not an SVG number")
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("not a finite SVG number")
	}
	return value, nil
}

func optionalSVGNumber(raw string) (float64, error) {
	if raw == "" {
		return 0, nil
	}
	return parseSVGNumber(raw)
}

func positiveSVGAttr(attrs map[string]string, key string) bool {
	value, err := parseSVGNumber(attrs[key])
	return err == nil && value > 0
}

type svgPoint struct{ x, y float64 }

func parseSVGPoints(raw string) ([]svgPoint, error) {
	values, err := parseSVGNumberList(raw)
	if err != nil || len(values)%2 != 0 {
		return nil, fmt.Errorf("must be coordinate pairs")
	}
	points := make([]svgPoint, 0, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		points = append(points, svgPoint{x: values[index], y: values[index+1]})
	}
	return points, nil
}

func parseSVGNumberList(raw string) ([]float64, error) {
	rest := strings.TrimSpace(raw)
	if rest == "" {
		return nil, fmt.Errorf("empty number list")
	}
	var values []float64
	for rest != "" {
		match := svgNumberPrefix.FindString(rest)
		if match == "" {
			return nil, fmt.Errorf("invalid number list")
		}
		value, err := parseSVGNumber(match)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		rest = rest[len(match):]
		if rest == "" {
			break
		}
		trimmed := strings.TrimLeft(rest, " \t\n\r")
		if strings.HasPrefix(trimmed, ",") {
			trimmed = strings.TrimLeft(trimmed[1:], " \t\n\r")
			if trimmed == "" || strings.HasPrefix(trimmed, ",") {
				return nil, fmt.Errorf("invalid comma in number list")
			}
		} else if len(trimmed) == len(rest) && trimmed[0] != '+' && trimmed[0] != '-' && trimmed[0] != '.' {
			return nil, fmt.Errorf("numbers are not separated")
		}
		rest = trimmed
	}
	return values, nil
}

func pointsContainDistinctPair(points []svgPoint) bool {
	if len(points) < 2 {
		return false
	}
	first := points[0]
	for _, point := range points[1:] {
		if point != first {
			return true
		}
	}
	return false
}
