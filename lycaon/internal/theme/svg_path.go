package theme

import (
	"fmt"
	"strings"
)

type svgPathToken struct {
	command byte
	number  float64
}

var svgPathArity = map[byte]int{
	'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4,
	'Q': 4, 'T': 2, 'A': 7, 'Z': 0,
}

func validateSVGPath(raw string) error {
	tokens, err := tokenizeSVGPath(raw)
	if err != nil {
		return err
	}
	if len(tokens) == 0 || upperSVGPathCommand(tokens[0].command) != 'M' {
		return fmt.Errorf("must start with moveto")
	}
	for index := 0; index < len(tokens); {
		rawCommand := tokens[index].command
		if rawCommand == 0 {
			return fmt.Errorf("numbers must follow a path command")
		}
		command := upperSVGPathCommand(rawCommand)
		index++
		start := index
		for index < len(tokens) && tokens[index].command == 0 {
			index++
		}
		count := index - start
		arity := svgPathArity[command]
		if arity == 0 {
			if count != 0 {
				return fmt.Errorf("closepath takes no numbers")
			}
			continue
		}
		if count < arity || count%arity != 0 {
			return fmt.Errorf("%c requires groups of %d numbers", command, arity)
		}
		if command == 'A' {
			for offset := start; offset < index; offset += arity {
				if tokens[offset].number < 0 || tokens[offset+1].number < 0 {
					return fmt.Errorf("arc radii must be non-negative")
				}
				if !svgArcFlag(tokens[offset+3].number) || !svgArcFlag(tokens[offset+4].number) {
					return fmt.Errorf("arc flags must be 0 or 1")
				}
			}
		}
	}
	if !svgPathHasVisibleSegment(tokens) {
		return fmt.Errorf("contains no drawing segment")
	}
	return nil
}

type svgPathPoint struct{ x, y float64 }

type svgPathCursor struct {
	current              svgPathPoint
	subpathStart         svgPathPoint
	lastCubicControl     svgPathPoint
	lastQuadraticControl svgPathPoint
	lastSegment          byte
	draws                bool
}

func svgPathHasVisibleSegment(tokens []svgPathToken) bool {
	var cursor svgPathCursor
	for index := 0; index < len(tokens); {
		rawCommand := tokens[index].command
		command := upperSVGPathCommand(rawCommand)
		relative := rawCommand >= 'a' && rawCommand <= 'z'
		index++
		start := index
		for index < len(tokens) && tokens[index].command == 0 {
			index++
		}
		cursor.apply(command, relative, tokens[start:index])
	}
	return cursor.draws
}

func (cursor *svgPathCursor) apply(command byte, relative bool, args []svgPathToken) {
	switch command {
	case 'M', 'L':
		cursor.applyPoints(command, relative, args)
	case 'H', 'V':
		cursor.applyAxis(command, relative, args)
	case 'C', 'S':
		cursor.applyCubic(command, relative, args)
	case 'Q', 'T':
		cursor.applyQuadratic(command, relative, args)
	case 'A':
		cursor.applyArcs(relative, args)
	case 'Z':
		cursor.draws = cursor.draws || cursor.current != cursor.subpathStart
		cursor.current = cursor.subpathStart
		cursor.lastSegment = command
	}
}

func (cursor *svgPathCursor) applyPoints(command byte, relative bool, args []svgPathToken) {
	for offset, pair := 0, 0; offset < len(args); offset, pair = offset+2, pair+1 {
		next := svgPathDestination(cursor.current, args[offset].number, args[offset+1].number, relative)
		if command == 'M' && pair == 0 {
			cursor.subpathStart = next
		} else {
			cursor.draws = cursor.draws || next != cursor.current
		}
		cursor.current = next
	}
	if command == 'M' && len(args) > 2 {
		cursor.lastSegment = 'L'
	} else {
		cursor.lastSegment = command
	}
}

func (cursor *svgPathCursor) applyAxis(command byte, relative bool, args []svgPathToken) {
	for _, arg := range args {
		value := arg.number
		if command == 'H' {
			if relative {
				value += cursor.current.x
			}
			cursor.draws = cursor.draws || value != cursor.current.x
			cursor.current.x = value
			continue
		}
		if relative {
			value += cursor.current.y
		}
		cursor.draws = cursor.draws || value != cursor.current.y
		cursor.current.y = value
	}
	cursor.lastSegment = command
}

func (cursor *svgPathCursor) applyCubic(command byte, relative bool, args []svgPathToken) {
	arity := svgPathArity[command]
	for offset := 0; offset < len(args); offset += arity {
		position := offset
		control1 := cursor.current
		if command == 'C' {
			control1 = svgPathDestination(cursor.current, args[position].number, args[position+1].number, relative)
			position += 2
		} else if cursor.lastSegment == 'C' || cursor.lastSegment == 'S' {
			control1 = svgPathPoint{2*cursor.current.x - cursor.lastCubicControl.x, 2*cursor.current.y - cursor.lastCubicControl.y}
		}
		control2 := svgPathDestination(cursor.current, args[position].number, args[position+1].number, relative)
		next := svgPathDestination(cursor.current, args[position+2].number, args[position+3].number, relative)
		cursor.draws = cursor.draws || control1 != cursor.current || control2 != cursor.current || next != cursor.current
		cursor.current = next
		cursor.lastCubicControl = control2
		cursor.lastSegment = command
	}
}

func (cursor *svgPathCursor) applyQuadratic(command byte, relative bool, args []svgPathToken) {
	arity := svgPathArity[command]
	for offset := 0; offset < len(args); offset += arity {
		position := offset
		control := cursor.current
		if command == 'Q' {
			control = svgPathDestination(cursor.current, args[position].number, args[position+1].number, relative)
			position += 2
		} else if cursor.lastSegment == 'Q' || cursor.lastSegment == 'T' {
			control = svgPathPoint{2*cursor.current.x - cursor.lastQuadraticControl.x, 2*cursor.current.y - cursor.lastQuadraticControl.y}
		}
		next := svgPathDestination(cursor.current, args[position].number, args[position+1].number, relative)
		cursor.draws = cursor.draws || control != cursor.current || next != cursor.current
		cursor.current = next
		cursor.lastQuadraticControl = control
		cursor.lastSegment = command
	}
}

func (cursor *svgPathCursor) applyArcs(relative bool, args []svgPathToken) {
	for offset := 0; offset < len(args); offset += svgPathArity['A'] {
		next := svgPathDestination(cursor.current, args[offset+5].number, args[offset+6].number, relative)
		cursor.draws = cursor.draws || next != cursor.current
		cursor.current = next
		cursor.lastSegment = 'A'
	}
}

func svgPathDestination(current svgPathPoint, x, y float64, relative bool) svgPathPoint {
	if relative {
		return svgPathPoint{current.x + x, current.y + y}
	}
	return svgPathPoint{x, y}
}

func tokenizeSVGPath(raw string) ([]svgPathToken, error) {
	rest := strings.TrimSpace(raw)
	if rest == "" {
		return nil, fmt.Errorf("path is empty")
	}
	var tokens []svgPathToken
	for rest != "" {
		if command, ok := svgPathCommand(rest[0]); ok {
			tokens = append(tokens, svgPathToken{command: command})
			rest = strings.TrimLeft(rest[1:], " \t\n\r")
			if strings.HasPrefix(rest, ",") {
				return nil, fmt.Errorf("comma cannot follow a path command")
			}
			continue
		}
		match := svgNumberPrefix.FindString(rest)
		if match == "" {
			return nil, fmt.Errorf("path contains invalid syntax near %q", pathPrefix(rest))
		}
		value, err := parseSVGNumber(match)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, svgPathToken{number: value})
		rest = rest[len(match):]
		if rest == "" {
			break
		}
		trimmed := strings.TrimLeft(rest, " \t\n\r")
		if strings.HasPrefix(trimmed, ",") {
			trimmed = strings.TrimLeft(trimmed[1:], " \t\n\r")
			if trimmed == "" || strings.HasPrefix(trimmed, ",") {
				return nil, fmt.Errorf("invalid comma in path")
			}
		}
		rest = trimmed
	}
	return tokens, nil
}

func svgPathCommand(raw byte) (byte, bool) {
	upper := upperSVGPathCommand(raw)
	_, ok := svgPathArity[upper]
	return raw, ok
}

func upperSVGPathCommand(raw byte) byte {
	if raw >= 'a' && raw <= 'z' {
		return raw - ('a' - 'A')
	}
	return raw
}

func svgArcFlag(value float64) bool { return value == 0 || value == 1 }

func pathPrefix(raw string) string {
	if len(raw) <= 12 {
		return raw
	}
	return raw[:12]
}
