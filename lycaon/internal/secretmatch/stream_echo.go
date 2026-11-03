package secretmatch

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
)

// StreamPolicy selects publication behavior without changing execution bytes.
type StreamPolicy struct{ ManagedEchoes bool }

// streamEncodedEchoes masks whole encoded quanta containing known managed bytes.
func (s *streamScreen) streamEncodedEchoes(ctx context.Context, text string, until int64) error {
	if !s.policy.ManagedEchoes {
		return nil
	}
	largest := 0
	for _, n := range s.needles {
		if IsManagedRule(n.value.RuleID) {
			largest = max(largest, len(n.value.Secret))
		}
	}
	s.echoWork = int64(len(text))
	charge := int64(len(text))*2 + int64(largest)*96 + 1024
	if err := s.memory.take(charge); err != nil {
		return err
	}
	defer s.memory.release(charge)
	for start := 0; start < len(text); {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !base64Byte(text[start]) {
			start++
			continue
		}
		end := start + 1
		for end < len(text) && base64Byte(text[end]) {
			end++
		}
		run := text[start:end]
		for padding := 0; padding < 2 && end < len(text) && text[end] == '='; padding++ {
			end++
		}
		if len(run) >= 8 && !(strings.ContainsAny(run, "-_") && strings.ContainsAny(run, "+/")) {
			encoding := base64.RawStdEncoding
			if strings.ContainsAny(run, "-_") {
				encoding = base64.RawURLEncoding
			}
			for offset := 0; offset < 4 && len(run)-offset >= 8; offset++ {
				encoded := run[offset:]
				if len(encoded)%4 == 1 {
					encoded = encoded[:len(encoded)-1]
				}
				s.echoWork += int64(len(encoded))
				if s.echoWork > s.bounds.DetectorWork {
					return &StreamLimitError{"screen_work_units", s.bounds.DetectorWork, s.echoWork}
				}
				decoded, err := encoding.DecodeString(encoded)
				if err != nil {
					clear(decoded)
					continue
				}
				err = s.matchDecodedEchoes(ctx, decoded, start+offset, start+offset+len(encoded), until)
				clear(decoded)
				if err != nil {
					return err
				}
			}
		}
		start = end
	}
	return nil
}

func (s *streamScreen) matchDecodedEchoes(ctx context.Context, decoded []byte, encodedStart, encodedEnd int, until int64) error {
	for _, n := range s.needles {
		if !IsManagedRule(n.value.RuleID) || !n.needle.whole || n.needle.text != n.value.Secret || len(n.value.Secret) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		s.echoWork += int64(len(decoded))
		if s.echoWork > s.bounds.DetectorWork {
			return &StreamLimitError{"screen_work_units", s.bounds.DetectorWork, s.echoWork}
		}
		secret := []byte(n.value.Secret)
		for cursor := 0; cursor < len(decoded); {
			at := bytes.Index(decoded[cursor:], secret)
			if at < 0 {
				break
			}
			at += cursor
			start := s.base + int64(encodedStart+(at/3)*4)
			end := s.base + int64(encodedStart+((at+len(secret)+2)/3)*4)
			end = min(end, s.base+int64(encodedEnd))
			if start >= s.emit && start < until {
				// Whole-value spellings retain the existing reference match.
				if at != 0 || len(decoded) != len(secret) {
					s.echoWork += int64(len(s.hits.values)) + int64(len(n.value.Secret))
					if s.echoWork > s.bounds.DetectorWork {
						return &StreamLimitError{"screen_work_units", s.bounds.DetectorWork, s.echoWork}
					}
					for i := range s.hits.values {
						hit := &s.hits.values[i]
						if hit.start < end && hit.end > start {
							hit.hit.Reference = ""
						}
					}
					v := n.value
					hit := Match{RuleID: v.RuleID, Title: v.Title, Severity: "high", GenericShape: GenericShape(v.Secret), Fingerprint: v.Fingerprint, VarName: v.Name, Container: v.Container, Source: v.Source, Retired: v.Retired, NonDisclosable: v.NonDisclosable}
					if err := s.hits.add(streamMatch{hit: hit, start: start, end: end}); err != nil {
						return err
					}
				}
			}
			cursor = at + len(secret)
		}
	}
	return nil
}

func base64Byte(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '+' || b == '/' || b == '-' || b == '_'
}
