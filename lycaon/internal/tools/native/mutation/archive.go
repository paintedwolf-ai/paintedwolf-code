package mutation

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// Archive utilities read through the invocation's read authority, extract
// through the shared extraction operation (confinement, link and governed-path
// guards, catalog entry/byte/expansion ceilings, recoverable writes) and
// publish created archives byte for byte through the pinned write door.

// archiveBudget bounds one creation or decompression by catalog ceilings.
type archiveBudget struct {
	entries, maxEntries int64
	bytes, maxBytes     int64
	limitEntries        string
	limitBytes          string
}

func (b *archiveBudget) entry() error {
	if b.entries++; b.entries > b.maxEntries {
		return limitRefusal(b.limitEntries, "entries", b.maxEntries, b.entries)
	}
	return nil
}

func (b *archiveBudget) add(n int64) error {
	if b.bytes += n; b.bytes > b.maxBytes {
		return limitRefusal(b.limitBytes, "bytes", b.maxBytes, b.bytes)
	}
	return nil
}

func limitRefusal(id, unit string, maximum, observed int64) error {
	return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": id, "resource_unit": unit, "maximum": maximum, "observed": observed}}
}

func (inv *invocation) createBudget() *archiveBudget {
	return &archiveBudget{maxEntries: inv.owners.limit("archive_create_entries"), maxBytes: inv.owners.limit("archive_create_input_bytes"),
		limitEntries: "archive_create_entries", limitBytes: "archive_create_input_bytes"}
}

// decodeLimit is the most bytes a compressed input of size n may expand to.
func (inv *invocation) decodeLimit(n int64) int64 {
	maximum := inv.owners.limit("archive_extract_bytes")
	if ratio := inv.owners.limit("archive_expansion_ratio"); n > 0 && n <= maximum/ratio {
		maximum = n * ratio
	}
	return maximum
}

// readBound reads one operand's bytes under read authority, refusing more
// than maximum without retaining them.
func (inv *invocation) readBound(operand string, maximum int64, limit string) ([]byte, os.FileInfo, error) {
	bound, err := inv.bind(operand, false)
	if err != nil {
		return nil, nil, err
	}
	f, err := bound.Open()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > maximum {
		return nil, nil, limitRefusal(limit, "bytes", maximum, int64(len(data)))
	}

	return data, info, nil
}

// publishCreated captures an output path and publishes created bytes there.
func (inv *invocation) publishCreated(operand string, data []byte, noReplace bool) (bool, error) {
	bound, err := inv.bind(operand, true)
	if err != nil {
		return false, err
	}
	target, err := inv.capture(bound.Resolved.Abs)
	if err != nil {
		return false, err
	}
	if noReplace && target.pin.Mode != toolcontract.AbsentRevision {
		inv.fail(operand, "already exists")
		return false, nil
	}
	if _, err := inv.owners.publish(inv.ctx, inv.tc, target, data); err != nil {
		return false, err
	}
	inv.effects++
	return true, nil
}

// sourceEntry is one file or directory an archive is created from.
type sourceEntry struct {
	name string
	dir  bool
	data []byte
	mode os.FileMode
}

// collectRule selects what an archive is created from.
type collectRule struct {
	// recursive descends into directories.
	recursive bool
	// exclude reports an entry left out by name; with prune, nothing beneath
	// an excluded directory is visited either.
	exclude func(name string) bool
	prune   bool
	// archive is the archive being written, which never contains itself.
	archive string
	// flatten stores files under their base names and leaves directories out.
	flatten bool
}

// collect walks operands under base in sorted byte order. Links are refused,
// never followed; an excluded link is left out before it is examined.
func (inv *invocation) collect(base string, operands []string, rule collectRule, budget *archiveBudget) ([]sourceEntry, error) {
	var out []sourceEntry
	stored := map[string]string{}
	var visit func(name string) error
	visit = func(name string) error {
		if err := inv.ctx.Err(); err != nil {
			return err
		}
		operand := name
		if base != "" && !filepath.IsAbs(name) {
			operand = filepath.Join(base, name)
		}
		bound, err := inv.bind(operand, false)
		if err != nil {
			return err
		}
		info, err := os.Lstat(bound.Resolved.Abs)
		if err != nil {
			return err
		}
		entryName := filepath.ToSlash(filepath.Clean(name))
		if info.IsDir() {
			entryName = strings.TrimSuffix(entryName, "/") + "/"
		}
		excluded := rule.exclude != nil && rule.exclude(entryName)
		if (excluded && (rule.prune || !info.IsDir())) || bound.Resolved.Abs == rule.archive {
			return nil
		}
		if err := budget.entry(); err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return inv.unsupportedForm(name, "regular files and directories; archive links are not created")
		case info.IsDir():
			if !excluded && !rule.flatten {
				out = append(out, sourceEntry{name: entryName, dir: true, mode: info.Mode().Perm()})
			}
			if !rule.recursive {
				return nil
			}
			children, err := os.ReadDir(bound.Resolved.Abs)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(children))
			for _, child := range children {
				names = append(names, child.Name())
			}
			sort.Strings(names)
			for _, child := range names {
				if err := visit(filepath.Join(name, child)); err != nil {
					return err
				}
			}
			return nil
		}
		if rule.flatten {
			flat := path.Base(entryName)
			if first, taken := stored[flat]; taken {
				return inv.usage(name, "distinct file names for -j; "+first+" is also stored as "+flat)
			}
			stored[flat] = entryName
			entryName = flat
		}
		data, _, err := inv.readBound(operand, budget.maxBytes-budget.bytes, budget.limitBytes)
		if err != nil {
			return err
		}
		if err := budget.add(int64(len(data))); err != nil {
			return err
		}
		out = append(out, sourceEntry{name: entryName, data: data, mode: info.Mode().Perm()})
		return nil
	}
	for _, operand := range operands {
		if err := visit(operand); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// directory resolves -C/-d or the invocation cwd.
func (inv *invocation) directory(key string) (string, error) {
	value, ok := inv.value(key)
	if !ok {
		return inv.paths.Cwd().Abs, nil
	}
	bound, err := inv.bind(value, true)
	if err != nil {
		return "", err
	}
	return bound.Resolved.Abs, nil
}
