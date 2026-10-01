package sourcetree

import "sort"

type IntentEntry struct {
	Address    Address
	Disclosure Disclosure
}

func (v *View) Intent() []IntentEntry {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]IntentEntry, 0, v.rules.count())
	visitRules(v.rules.root, func(value ruleValue) {
		if value.derived {
			return
		}
		disclosure := Disclosure{Open: value.open}
		if value.hasRecursive {
			out = append(out, IntentEntry{value.address, Disclosure{Open: value.recursive, Recursive: true}})
			if value.recursive == value.open {
				return
			}
		}
		out = append(out, IntentEntry{value.address, disclosure})
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Address.Root != out[j].Address.Root {
			return out[i].Address.Root < out[j].Address.Root
		}
		if out[i].Address.Path != out[j].Address.Path {
			return out[i].Address.Path < out[j].Address.Path
		}
		return out[i].Disclosure.Recursive && !out[j].Disclosure.Recursive
	})
	return out
}

func (v *View) LoadingDirectories() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.loading)
}
