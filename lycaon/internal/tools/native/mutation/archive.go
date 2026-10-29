package mutation

import (
	"io"
	"os"
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
	if _, err := inv.owners.publish(inv.ctx, inv.tc, inv.barrier, target, data); err != nil {
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

// collect walks operands under base in sorted byte order. Directories are
// descended only when recursive; links are refused, never followed.
func (inv *invocation) collect(base string, operands []string, recursive bool, budget *archiveBudget) ([]sourceEntry, error) {
	var out []sourceEntry
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
		if err := budget.entry(); err != nil {
			return err
		}
		entryName := filepath.ToSlash(filepath.Clean(name))
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return inv.unsupportedForm(name, "regular files and directories; archive links are not created")
		case info.IsDir():
			out = append(out, sourceEntry{name: strings.TrimSuffix(entryName, "/") + "/", dir: true, mode: info.Mode().Perm()})
			if !recursive {
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

// boundedReader refuses decoded bytes past the expansion ceiling.
type boundedReader struct {
	r         io.Reader
	remaining int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if b.remaining -= int64(n); b.remaining < 0 {
		return 0, limitRefusal("archive_extract_bytes", "bytes", 0, -b.remaining)
	}
	return n, err
}
