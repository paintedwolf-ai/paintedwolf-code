package mutation

import (
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/pattern"
)

// memberPatterns are archive member globs. * and ? also match /, as tar
// exclusions and zip and unzip wildcards do.
type memberPatterns []*regexp.Regexp

func (inv *invocation) memberPatterns(option string, words []string) (memberPatterns, error) {
	out := make(memberPatterns, 0, len(words))
	for _, word := range words {
		expr, err := pattern.Regexp(strings.TrimPrefix(word, "./"), pattern.EntireString)
		if err != nil {
			return nil, inv.usage(word, "a shell glob for "+option)
		}
		re, err := regexp.Compile("(?s)" + expr)
		if err != nil {
			return nil, inv.usage(word, "a shell glob for "+option)
		}
		out = append(out, re)
	}
	return out, nil
}

// matchName reports a glob matching the whole member name.
func (p memberPatterns) matchName(name string) bool {
	name = strings.TrimPrefix(name, "./")
	for _, re := range p {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// matchComponents reports a glob matching any run of whole components of the
// name: from its start or after any slash, through its end or up to any
// later slash. A pattern naming a directory therefore excludes what lies
// beneath it wherever the directory appears.
func (p memberPatterns) matchComponents(name string) bool {
	name = strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	if len(p) == 0 || name == "" {
		return false
	}
	starts := []int{0}
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			starts = append(starts, i+1)
		}
	}
	for _, start := range starts {
		for end := start; end <= len(name); end++ {
			if end != len(name) && name[end] != '/' {
				continue
			}
			for _, re := range p {
				if re.MatchString(name[start:end]) {
					return true
				}
			}
		}
	}
	return false
}

// vcsNames are the version control files and directories tar --exclude-vcs
// leaves out.
var vcsNames = []string{
	"CVS", ".cvsignore", "RCS", "SCCS", ".svn", ".git", ".gitignore", ".gitattributes", ".gitmodules",
	".arch-ids", "{arch}", "=RELEASE-ID", "=meta-update", "=update",
	".bzr", ".bzrignore", ".bzrtags", ".hg", ".hgignore", ".hgtags", "_darcs",
}

// listFile reads one name per line from FILE through read authority; empty
// lines are skipped and a final CR is kept, as tar reads them.
func (inv *invocation) listFile(file string) ([]string, error) {
	data, _, err := inv.readBound(file, inv.owners.limit("stream_bytes"), "stream_bytes")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}
