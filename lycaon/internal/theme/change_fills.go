package theme

// CodeInkMin is the contrast code ink keeps over a change fill.
const CodeInkMin = 4.5

// CodeInkRetention is the share of its page contrast an ink keeps over a
// change fill when the theme already sets it under CodeInkMin.
const CodeInkRetention = 0.7

// ChangeFillShape sets the target strength and line share.
type ChangeFillShape struct {
	// Strength caps the hue's alpha across a changed word over its line.
	Strength float64
	// LineShare is the part of the fitted strength the line fill carries.
	LineShare float64
}

// Dark fills separate less at the same alpha, so the word layer takes more.
var changeFillShapes = map[Appearance]ChangeFillShape{
	AppearanceLight: {Strength: 0.30, LineShare: 0.4},
	AppearanceDark:  {Strength: 0.36, LineShare: 0.3},
}

// ChangeFillShapeFor returns an appearance's fitting targets.
func ChangeFillShapeFor(appearance Appearance) ChangeFillShape {
	return changeFillShapes[appearance]
}

type changeFamily struct{ hue, line, word string }

var changeFamilies = []changeFamily{
	{hue: "diff-add-hue", line: "diff-add-line", word: "diff-add-word"},
	{hue: "diff-delete-hue", line: "diff-delete-line", word: "diff-delete-word"},
}

// changeFitSteps resolves beyond 8-bit alpha precision.
const changeFitSteps = 24

// fitChangeFills fits line and word fills together against every resolved syntax ink.
func fitChangeFills(tokens map[string]Color, syntax map[string]SyntaxStyle, appearance Appearance) {
	plate := flatten(tokens["surface"], tokens["background"])
	inks := codeInks(tokens, syntax, plate)
	shape := changeFillShapes[appearance]
	for _, family := range changeFamilies {
		layers := fitChangeLayers(tokens[family.hue], plate, inks, shape)
		tokens[family.line], tokens[family.word] = layers.line, layers.word
	}
}

type codeInk struct {
	color Color
	min   float64
}

func codeInks(tokens map[string]Color, syntax map[string]SyntaxStyle, plate Color) []codeInk {
	inks := []codeInk{inkOn(tokens["text"], plate)}
	for _, scope := range syntaxScopes {
		inks = append(inks, inkOn(syntax[scope.ID].Color, plate))
	}
	return inks
}

func inkOn(ink, plate Color) codeInk {
	return codeInk{color: ink, min: min(CodeInkMin, CodeInkRetention*contrastRatio(ink, plate, plate))}
}

type changeLayers struct{ line, word Color }

func fitChangeLayers(hue, plate Color, inks []codeInk, shape ChangeFillShape) changeLayers {
	if top := layersAt(hue, shape.Strength, shape.LineShare); legibleUnder(top, plate, inks) {
		return top
	}
	// Zero fill is the valid lower bound.
	best := layersAt(hue, 0, shape.LineShare)
	lo, hi := 0.0, shape.Strength
	for i := 0; i < changeFitSteps; i++ {
		mid := (lo + hi) / 2
		candidate := layersAt(hue, mid, shape.LineShare)
		if legibleUnder(candidate, plate, inks) {
			lo, best = mid, candidate
			continue
		}
		hi = mid
	}
	return best
}

func layersAt(hue Color, strength, lineShare float64) changeLayers {
	line := strength * lineShare
	word := 0.0
	if line < 1 {
		word = (strength - line) / (1 - line)
	}
	return changeLayers{
		line: mix(hue, line*100, transparentInk),
		word: mix(hue, word*100, transparentInk),
	}
}

func legibleUnder(layers changeLayers, plate Color, inks []codeInk) bool {
	line := compositeOver(layers.line, plate)
	word := compositeOver(layers.word, line)
	for _, ink := range inks {
		if contrastRatio(ink.color, line, line) < ink.min || contrastRatio(ink.color, word, word) < ink.min {
			return false
		}
	}
	return true
}

func checkChangeFills(compiled Compiled) []Fault {
	plate := flatten(compiled.Tokens["surface"], compiled.Tokens["background"])
	inks := codeInks(compiled.Tokens, compiled.Syntax, plate)
	var faults []Fault
	for _, family := range changeFamilies {
		layers := changeLayers{line: compiled.Tokens[family.line], word: compiled.Tokens[family.word]}
		if !legibleUnder(layers, plate, inks) {
			faults = append(faults, Fault{
				Field:   "tokens." + family.hue,
				Message: "a change fill leaves a syntax ink under its code floor",
			})
		}
	}
	return faults
}
