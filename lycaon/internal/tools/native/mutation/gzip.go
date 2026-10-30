package mutation

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// runGzip replaces each PATH with PATH.gz; the original is removed through
// the recoverable delete only after the compressed file is published. -c
// writes to stdout and keeps inputs, -k keeps inputs, -f replaces an existing
// output, -d decompresses and -l lists. Without PATH it filters stdin.
func runGzip(inv *invocation) error {
	if inv.flag("-d") || inv.flag("-t") {
		return runGunzip(inv)
	}
	level := inv.gzipLevel()
	if inv.flag("-l") {
		return inv.gzipList()
	}
	if len(inv.operands()) == 0 {
		data, err := inv.stdinBounded("archive_create_input_bytes")
		if err != nil {
			return err
		}
		compressed, err := gzipMember(data, "", time.Time{}, level)
		if err != nil {
			return err
		}
		_, err = inv.stdout.Write(compressed)
		return err
	}
	toStdout := inv.flag("-c")
	return inv.each(inv.operands(), func(operand string) (int, error) {
		if !toStdout && strings.HasSuffix(operand, ".gz") {
			inv.fail(operand, "already has .gz suffix -- unchanged")
			return 0, nil
		}
		data, info, err := inv.readBound(operand, inv.owners.limit("archive_create_input_bytes"), "archive_create_input_bytes")
		if err != nil {
			return 0, err
		}
		name, modified := filepath.Base(operand), info.ModTime()
		if inv.flag("-n") {
			name, modified = "", time.Time{}
		}
		compressed, err := gzipMember(data, name, modified, level)
		if err != nil {
			return 0, err
		}
		if toStdout {
			_, err := inv.stdout.Write(compressed)
			return 0, err
		}
		return inv.replaceCompressed(operand, operand+".gz", compressed)
	})
}

// gzipMember writes one member as GNU gzip does: the stored name and time of
// a file input, the Unix OS byte, and no name or time for a stream.
func gzipMember(data []byte, name string, modified time.Time, level int) ([]byte, error) {
	var out bytes.Buffer
	gz, err := gzip.NewWriterLevel(&out, level)
	if err != nil {
		return nil, err
	}
	gz.Name, gz.ModTime, gz.OS = name, modified, 3
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// gzipLevel is the last of -1 through -9, --fast and --best; GNU and BSD
// gzip both default to 6.
func (inv *invocation) gzipLevel() int {
	level := 6
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case "-1":
			level = int(item.Option.Spelling[1] - '0')
		case "--fast":
			level = 1
		case "--best":
			level = 9
		}
	}
	return level
}

func (inv *invocation) stdinBounded(limit string) ([]byte, error) {
	maximum := inv.owners.limit(limit)
	data, err := io.ReadAll(io.LimitReader(inv.input, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, limitRefusal(limit, "bytes", maximum, int64(len(data)))
	}
	return data, nil
}

// gzipSuffixes are the compressed names gunzip restores, longest first.
var gzipSuffixes = []struct{ compressed, restored string }{{".tgz", ".tar"}, {".gz", ""}}

func gunzipOutput(operand string) (string, bool) {
	for _, suffix := range gzipSuffixes {
		if stem, ok := strings.CutSuffix(operand, suffix.compressed); ok && stem != "" && !strings.HasSuffix(stem, "/") {
			return stem + suffix.restored, true
		}
	}
	return "", false
}

func runGunzip(inv *invocation) error {
	if inv.flag("-l") {
		return inv.gzipList()
	}
	if len(inv.operands()) == 0 {
		data, err := inv.stdinBounded("archive_input_bytes")
		if err != nil {
			return err
		}
		decoded, err := inv.gunzipBytes(data)
		if err != nil {
			return err
		}
		_, err = inv.stdout.Write(decoded)
		return err
	}
	toStdout, testOnly := inv.flag("-c"), inv.flag("-t")
	return inv.each(inv.operands(), func(operand string) (int, error) {
		if testOnly {
			if _, err := inv.gunzipFile(operand); err != nil {
				if _, ok := ordinary(err); ok || toolrejection.AsToolReject(err) != nil {
					return 0, err
				}
				inv.fail(operand, "invalid compressed data--format violated")
			}
			return 0, nil
		}
		output, ok := gunzipOutput(operand)
		if !ok && !toStdout {
			inv.fail(operand, "unknown suffix -- ignored")
			return 0, nil
		}
		decoded, err := inv.gunzipFile(operand)
		if err != nil {
			return 0, err
		}
		if toStdout {
			_, err := inv.stdout.Write(decoded)
			return 0, err
		}
		return inv.replaceCompressed(operand, output, decoded)
	})
}

// gzipList prints GNU gzip -l: compressed and uncompressed sizes, the ratio
// of bytes saved beyond the header and trailer, and the restored name.
func (inv *invocation) gzipList() error {
	operands := inv.operands()
	if len(operands) == 0 {
		return inv.usage(inv.name(), "PATH ... to list")
	}
	_, _ = fmt.Fprintf(inv.stdout, "%19s %19s  ratio uncompressed_name\n", "compressed", "uncompressed")
	var totalIn, totalOut, totalHeader int64
	listed := 0
	err := inv.each(operands, func(operand string) (int, error) {
		data, _, err := inv.readBound(operand, inv.owners.limit("archive_input_bytes"), "archive_input_bytes")
		if err != nil {
			return 0, err
		}
		header, ok := gzipHeaderBytes(data)
		if !ok {
			inv.fail(operand, "not in gzip format")
			return 0, nil
		}
		in := int64(len(data))
		out := int64(binary.LittleEndian.Uint32(data[len(data)-4:]))
		name, ok := gunzipOutput(operand)
		if !ok {
			name = operand
		}
		_, err = fmt.Fprintf(inv.stdout, "%19d %19d %s %s\n", in, out, gzipRatio(out-(in-header), out), name)
		totalIn, totalOut, totalHeader = totalIn+in, totalOut+out, totalHeader+header
		listed++
		return 0, err
	})
	if err == nil && listed > 1 {
		_, err = fmt.Fprintf(inv.stdout, "%19d %19d %s (totals)\n", totalIn, totalOut, gzipRatio(totalOut-(totalIn-totalHeader), totalOut))
	}
	return err
}

// gzipHeaderBytes is the member header length plus the eight trailer bytes.
func gzipHeaderBytes(data []byte) (int64, bool) {
	if len(data) < 18 || data[0] != 0x1f || data[1] != 0x8b || data[2] != 8 {
		return 0, false
	}
	flags, at := data[3], 10
	if flags&0x04 != 0 {
		if at+2 > len(data) {
			return 0, false
		}
		at += 2 + int(binary.LittleEndian.Uint16(data[at:]))
	}
	for _, bit := range []byte{0x08, 0x10} {
		if flags&bit != 0 {
			end := bytes.IndexByte(data[min(at, len(data)):], 0)
			if end < 0 {
				return 0, false
			}
			at += end + 1
		}
	}
	if flags&0x02 != 0 {
		at += 2
	}
	if at+8 > len(data) {
		return 0, false
	}
	return int64(at + 8), true
}

// gzipRatio is GNU gzip's display_ratio: tenths of a percent, signed.
func gzipRatio(num, den int64) string {
	var ratio int64
	switch {
	case den == 0:
	case den < 2147483:
		ratio = 1000 * num / den
	default:
		ratio = num / (den / 1000)
	}
	sign := " "
	if ratio < 0 {
		sign, ratio = "-", -ratio
	}
	return fmt.Sprintf("%s%2d.%1d%%", sign, ratio/10, ratio%10)
}

func (inv *invocation) gunzipFile(operand string) ([]byte, error) {
	data, _, err := inv.readBound(operand, inv.owners.limit("archive_input_bytes"), "archive_input_bytes")
	if err != nil {
		return nil, err
	}
	return inv.gunzipBytes(data)
}

func (inv *invocation) gunzipBytes(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()
	return io.ReadAll(&boundedReader{r: gz, remaining: inv.decodeLimit(int64(len(data)))})
}

// replaceCompressed publishes the new file, refusing to replace an existing
// one unless -f, then recoverably removes the input unless -k.
func (inv *invocation) replaceCompressed(input, output string, data []byte) (int, error) {
	published, err := inv.publishCreated(output, data, !inv.flag("-f"))
	if err != nil || !published {
		return 0, err
	}
	if inv.flag("-k") {
		return 1, nil
	}
	bound, err := inv.bind(input, true)
	if err != nil {
		return 1, err
	}
	result, err := inv.owners.Operations.Delete.Delete(inv.ctx, native.DeleteInput{Paths: []string{bound.Resolved.Abs}, FilesOnly: true}, inv.tc)
	return 1 + len(result.Deleted), err
}

func runZcat(inv *invocation) error {
	if len(inv.operands()) == 0 {
		data, err := inv.stdinBounded("archive_input_bytes")
		if err != nil {
			return err
		}
		decoded, err := inv.gunzipBytes(data)
		if err != nil {
			return err
		}
		_, err = inv.stdout.Write(decoded)
		return err
	}
	return inv.each(inv.operands(), func(operand string) (int, error) {
		decoded, err := inv.gunzipFile(operand)
		if err != nil {
			return 0, err
		}
		_, err = inv.stdout.Write(decoded)
		return 0, err
	})
}
