package budget

import "github.com/zricethezav/gitleaks/v8/regexp"

func (m *Meter) StringIndices(re *regexp.Regexp, value string) [][]int {
	n, finish := m.enumeration(len(value), 256)
	out := re.FindAllStringIndex(value, n)
	finish(len(out))
	return out
}
func (m *Meter) StringSubmatchIndices(re *regexp.Regexp, value string) [][]int {
	n, finish := m.enumeration(len(value), 256+32*(re.NumSubexp()+1))
	out := re.FindAllStringSubmatchIndex(value, n)
	finish(len(out))
	return out
}
func (m *Meter) Strings(re *regexp.Regexp, value string) []string {
	n, finish := m.enumeration(len(value), 256)
	out := re.FindAllString(value, n)
	finish(len(out))
	return out
}
func (m *Meter) StringSubmatches(re *regexp.Regexp, value string) [][]string {
	n, finish := m.enumeration(len(value), 256+64*(re.NumSubexp()+1))
	out := re.FindAllStringSubmatch(value, n)
	finish(len(out))
	return out
}
func (m *Meter) SubmatchIndices(re *regexp.Regexp, value []byte) [][]int {
	n, finish := m.enumeration(len(value), 256+32*(re.NumSubexp()+1))
	out := re.FindAllSubmatchIndex(value, n)
	finish(len(out))
	return out
}
