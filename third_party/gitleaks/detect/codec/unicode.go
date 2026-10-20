package codec

import (
	"bytes"
	"github.com/zricethezav/gitleaks/v8/detect/budget"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zricethezav/gitleaks/v8/regexp"
)

var (
	// Standard Unicode notation (e.g., U+1234)
	unicodeCodePointPat = regexp.MustCompile(`U\+([a-fA-F0-9]{4}).?`)

	// Multiple code points pattern - used for continuous sequences like "U+0074 U+006F U+006B..."
	unicodeMultiCodePointPat = regexp.MustCompile(`(?:U\+[a-fA-F0-9]{4}(?:\s|$))+`)

	// Common escape sequence used in programming languages (e.g., \u1234)
	unicodeEscapePat = regexp.MustCompile(`(?i)\\{1,2}u([a-fA-F0-9]{4})`)

	// Multiple escape sequences pattern - used for continuous sequences like "\u0074\u006F\u006B..."
	unicodeMultiEscapePat = regexp.MustCompile(`(?i)(?:\\{1,2}u[a-fA-F0-9]{4})+`)
)

// Unicode characters are encoded as 1 to 4 bytes per rune.
const maxBytesPerRune = 4

// decodeUnicode decodes Unicode escape sequences in the given string
func decodeUnicode(encodedValue string) string {
	return decodeUnicodeBudget(encodedValue, nil)
}
func decodeUnicodeBudget(encodedValue string, meter *budget.Meter) string {
	meter.Check()
	meter.Allocate(int64(len(encodedValue))*32 + 128)
	meter.Work(int64(len(encodedValue)))
	// First, check if we have a continuous sequence of Unicode code points
	if matches := meter.Strings(unicodeMultiCodePointPat, encodedValue); len(matches) > 0 {
		// For each detected sequence of code points
		meter.Count(len(matches))
		for _, match := range matches {
			meter.Check()
			meter.Allocate(int64(len(encodedValue)) * 2)
			meter.Work(int64(len(encodedValue)))
			// Decode the entire sequence at once
			decodedSequence := decodeMultiCodePointBudget(match, meter)

			// If we successfully decoded something, replace it in the original string
			if decodedSequence != "" && decodedSequence != match {
				encodedValue = strings.Replace(encodedValue, match, decodedSequence, 1)
			}
		}
		return encodedValue
	}

	// Next, check if we have a continuous sequence of escape sequences
	if matches := meter.Strings(unicodeMultiEscapePat, encodedValue); len(matches) > 0 {
		// For each detected sequence of escape sequences
		meter.Count(len(matches))
		for _, match := range matches {
			meter.Check()
			meter.Allocate(int64(len(encodedValue)) * 2)
			meter.Work(int64(len(encodedValue)))
			// Decode the entire sequence at once
			decodedSequence := decodeMultiEscapeBudget(match, meter)

			// If we successfully decoded something, replace it in the original string
			if decodedSequence != "" && decodedSequence != match {
				encodedValue = strings.Replace(encodedValue, match, decodedSequence, 1)
			}
		}
		return encodedValue
	}

	// If no multi-patterns were matched, fall back to the original implementation
	// for individual code points and escape sequences

	// Create a copy of the input to work with
	data := []byte(encodedValue)

	// Store the result
	var result []byte

	// Check and decode Unicode code points (U+1234 format)
	if unicodeCodePointPat.Match(data) {
		result = decodeIndividualCodePointsBudget(data, meter)
	}

	// If no code points were found or we have a mix of formats,
	// also check for Unicode escape sequences (\u1234 format)
	if len(result) == 0 || unicodeEscapePat.Match(data) {
		// If we already have some result from code point decoding,
		// continue decoding escape sequences on that result
		if len(result) > 0 {
			result = decodeIndividualEscapesBudget(result, meter)
		} else {
			result = decodeIndividualEscapesBudget(data, meter)
		}
	}

	// If nothing was decoded, return original string
	if len(result) == 0 || bytes.Equal(result, data) {
		return encodedValue
	}

	return string(result)
}

// decodeMultiCodePoint decodes a continuous sequence of Unicode code points (U+XXXX format)
func decodeMultiCodePoint(sequence string) string {
	return decodeMultiCodePointBudget(sequence, nil)
}
func decodeMultiCodePointBudget(sequence string, meter *budget.Meter) string {
	meter.Check()
	meter.Allocate(int64(len(sequence))*32 + 128)
	meter.Work(int64(len(sequence)))
	// If the sequence is empty, return empty string
	if sequence == "" {
		return ""
	}

	// Split the sequence by whitespace to get individual code points
	codePoints := strings.Fields(sequence)
	if len(codePoints) == 0 {
		return sequence
	}

	// Decode each code point and build the result
	var decodedRunes []rune
	for _, cp := range codePoints {
		meter.Check()
		// Check if it follows the U+XXXX pattern
		if !strings.HasPrefix(cp, "U+") || len(cp) < 6 {
			continue
		}

		// Extract the hexadecimal value
		hexValue := cp[2:]

		// Parse the hexadecimal value to an integer
		unicodeInt, err := strconv.ParseInt(hexValue, 16, 32)
		if err != nil {
			continue
		}

		// Convert to rune and add to result
		decodedRunes = append(decodedRunes, rune(unicodeInt))
	}

	// If we didn't decode anything, return the original sequence
	if len(decodedRunes) == 0 {
		return sequence
	}

	// Return the decoded string
	return string(decodedRunes)
}

// decodeMultiEscape decodes a continuous sequence of Unicode escape sequences (\uXXXX format)
func decodeMultiEscape(sequence string) string {
	return decodeMultiEscapeBudget(sequence, nil)
}
func decodeMultiEscapeBudget(sequence string, meter *budget.Meter) string {
	meter.Check()
	meter.Allocate(int64(len(sequence))*32 + 128)
	meter.Work(int64(len(sequence)))
	// If the sequence is empty, return empty string
	if sequence == "" {
		return ""
	}

	// Find all escape sequences
	escapes := meter.StringSubmatches(unicodeEscapePat, sequence)
	meter.Count(len(escapes))
	if len(escapes) == 0 {
		return sequence
	}

	// Decode each escape sequence and build the result
	var decodedRunes []rune
	for _, esc := range escapes {
		meter.Check()
		// Extract the hexadecimal value
		hexValue := esc[1]

		// Parse the hexadecimal value to an integer
		unicodeInt, err := strconv.ParseInt(hexValue, 16, 32)
		if err != nil {
			continue
		}

		// Convert to rune and add to result
		decodedRunes = append(decodedRunes, rune(unicodeInt))
	}

	// If we didn't decode anything, return the original sequence
	if len(decodedRunes) == 0 {
		return sequence
	}

	// Return the decoded string
	return string(decodedRunes)
}

// decodeIndividualCodePoints decodes individual Unicode code points (U+1234 format)
// This is a fallback for when we don't have a continuous sequence of code points
func decodeIndividualCodePoints(input []byte) []byte {
	return decodeIndividualCodePointsBudget(input, nil)
}
func decodeIndividualCodePointsBudget(input []byte, meter *budget.Meter) []byte {
	meter.Check()
	meter.Allocate(int64(len(input))*32 + 128)
	meter.Work(int64(len(input)))
	// Find all Unicode code point sequences in the input byte slice
	indices := meter.SubmatchIndices(unicodeCodePointPat, input)

	// If none found, return original input
	meter.Count(len(indices))
	if len(indices) == 0 {
		return input
	}

	// Iterate over found indices in reverse order to avoid modifying the slice length
	utf8Bytes := make([]byte, maxBytesPerRune)
	for i := len(indices) - 1; i >= 0; i-- {
		meter.Allocate(int64(len(input))*4 + 32)
		meter.Work(int64(len(input)))
		matches := indices[i]

		startIndex := matches[0]
		endIndex := matches[1]
		hexStartIndex := matches[2]
		hexEndIndex := matches[3]

		// If the input is like `U+1234 U+5678` we should replace `U+1234 `.
		// Otherwise, we should only replace `U+1234`.
		if endIndex != hexEndIndex && endIndex < len(input) && input[endIndex-1] == ' ' {
			endIndex = endIndex - 1
		}

		// Extract the hexadecimal value from the escape sequence
		hexValue := string(input[hexStartIndex:hexEndIndex])

		// Parse the hexadecimal value to an integer
		unicodeInt, err := strconv.ParseInt(hexValue, 16, 32)
		if err != nil {
			// If there's an error, continue to the next escape sequence
			continue
		}

		// Convert the Unicode code point to a UTF-8 representation
		utf8Len := utf8.EncodeRune(utf8Bytes, rune(unicodeInt))

		// Replace the escape sequence with the UTF-8 representation
		input = append(input[:startIndex], append(utf8Bytes[:utf8Len], input[endIndex:]...)...)
	}

	return input
}

// decodeIndividualEscapes decodes individual Unicode escape sequences (\u1234 format)
// This is a fallback for when we don't have a continuous sequence of escape sequences
func decodeIndividualEscapes(input []byte) []byte {
	return decodeIndividualEscapesBudget(input, nil)
}
func decodeIndividualEscapesBudget(input []byte, meter *budget.Meter) []byte {
	meter.Check()
	meter.Allocate(int64(len(input))*32 + 128)
	meter.Work(int64(len(input)))
	// Find all Unicode escape sequences in the input byte slice
	indices := meter.SubmatchIndices(unicodeEscapePat, input)

	// If none found, return original input
	meter.Count(len(indices))
	if len(indices) == 0 {
		return input
	}

	// Iterate over found indices in reverse order to avoid modifying the slice length
	utf8Bytes := make([]byte, maxBytesPerRune)
	for i := len(indices) - 1; i >= 0; i-- {
		meter.Allocate(int64(len(input))*4 + 32)
		meter.Work(int64(len(input)))
		matches := indices[i]

		startIndex := matches[0]
		hexStartIndex := matches[2]
		endIndex := matches[3]

		// Extract the hexadecimal value from the escape sequence
		hexValue := string(input[hexStartIndex:endIndex])

		// Parse the hexadecimal value to an integer
		unicodeInt, err := strconv.ParseInt(hexValue, 16, 32)
		if err != nil {
			// If there's an error, continue to the next escape sequence
			continue
		}

		// Convert the Unicode code point to a UTF-8 representation
		utf8Len := utf8.EncodeRune(utf8Bytes, rune(unicodeInt))

		// Replace the escape sequence with the UTF-8 representation
		input = append(input[:startIndex], append(utf8Bytes[:utf8Len], input[endIndex:]...)...)
	}

	return input
}
