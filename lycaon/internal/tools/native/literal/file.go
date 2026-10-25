package literal

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// signature is one versioned magic entry from the catalog corpus.
type signature struct {
	Name        string `json:"name"`
	PrefixHex   string `json:"prefix_hex"`
	Description string `json:"description"`
	MIME        string `json:"mime"`
	Minimum     int    `json:"minimum_identification_bytes"`
	prefix      []byte
}

// signatures is the frozen file identification corpus (signatures.json).
type signatures struct {
	Version int         `json:"version"`
	Corpus  []signature `json:"corpus"`
}

func decodeSignatures(raw json.RawMessage) (signatures, error) {
	var wrapper struct {
		FileSignatures json.RawMessage `json:"file_signatures"`
	}
	var out signatures
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return out, err
	}
	if err := json.Unmarshal(wrapper.FileSignatures, &out); err != nil {
		return out, err
	}
	if out.Version != 1 || len(out.Corpus) == 0 {
		return out, fmt.Errorf("unsupported file signature corpus version %d", out.Version)
	}
	for i := range out.Corpus {
		prefix, err := hex.DecodeString(out.Corpus[i].PrefixHex)
		if err != nil {
			return out, fmt.Errorf("signature %s: %w", out.Corpus[i].Name, err)
		}
		out.Corpus[i].prefix = prefix
	}
	return out, nil
}

// identification is file's answer: a description and a MIME type.
type identification struct{ description, mime, charset string }

var (
	identifyEmpty   = identification{description: "empty", mime: "application/x-empty"}
	identifyText    = identification{description: "UTF-8 Unicode text", mime: "text/plain", charset: "utf-8"}
	identifyASCII   = identification{description: "ASCII text", mime: "text/plain", charset: "us-ascii"}
	identifyData    = identification{description: "data", mime: "application/octet-stream"}
	identifyTar     = identification{description: "POSIX tar archive", mime: "application/x-tar"}
	identifyGIF87   = identification{description: "GIF image data", mime: "image/gif"}
	identifyMachO   = identification{description: "Mach-O executable", mime: "application/x-mach-binary"}
	identifyMachO64 = identification{description: "Mach-O 64-bit executable", mime: "application/x-mach-binary"}
	identifyMachOU  = identification{description: "Mach-O universal binary", mime: "application/x-mach-binary"}
)

// identify applies the corpus precedence: complete magic or structural
// signatures, then self-identifying encodings, then valid UTF-8 text, then
// opaque data. A truncated known prefix is never reported as that format.
func (s signatures) identify(data []byte) identification {
	if len(data) == 0 {
		return identifyEmpty
	}
	if id, ok := structural(data); ok {
		return id
	}
	for _, entry := range s.Corpus {
		if entry.Name == "pe" {
			if peExecutable(data) {
				return identification{description: entry.Description, mime: entry.MIME}
			}
			continue
		}
		if len(data) >= max(entry.Minimum, len(entry.prefix)) && bytes.HasPrefix(data, entry.prefix) {
			return identification{description: entry.Description, mime: entry.MIME}
		}
	}
	if utf8.Valid(data) && bytes.IndexByte(data, 0) < 0 {
		for _, b := range data {
			if b >= utf8.RuneSelf {
				return identifyText
			}
		}
		return identifyASCII
	}
	return identifyData
}

// structural recognizes the corpus's structural signatures.
func structural(data []byte) (identification, bool) {
	if len(data) >= 6 && bytes.HasPrefix(data, []byte("GIF87a")) {
		return identifyGIF87, true
	}
	if len(data) >= 32 {
		switch binary.BigEndian.Uint32(data) {
		case 0xfeedface, 0xcefaedfe:
			return identifyMachO, true
		case 0xfeedfacf:
			return identifyMachO64, true
		case 0xcafebabe:
			// Java class files share the fat magic; fat headers count architectures.
			if n := binary.BigEndian.Uint32(data[4:]); n > 0 && n < 32 {
				return identifyMachOU, true
			}
		}
	}
	if tarHeader(data) {
		return identifyTar, true
	}
	return identification{}, false
}

// tarHeader checks the ustar magic at offset 257 and the header checksum.
func tarHeader(data []byte) bool {
	if len(data) < 512 || !bytes.Equal(data[257:262], []byte("ustar")) {
		return false
	}
	field := bytes.TrimRight(bytes.TrimLeft(data[148:156], " "), " \x00")
	recorded, err := strconv.ParseUint(string(field), 8, 32)
	if err != nil {
		return false
	}
	sum := uint64(0)
	for i, b := range data[:512] {
		if i >= 148 && i < 156 {
			b = ' '
		}
		sum += uint64(b)
	}
	return sum == recorded
}

// peExecutable requires MZ, a bounded e_lfanew and the PE signature.
func peExecutable(data []byte) bool {
	if len(data) < 88 || data[0] != 'M' || data[1] != 'Z' {
		return false
	}
	offset := int64(binary.LittleEndian.Uint32(data[0x3c:]))
	return offset >= 0x40 && offset+4 <= int64(len(data)) && bytes.Equal(data[offset:offset+4], []byte("PE\x00\x00"))
}

func runFile(inv *invocation) error {
	operands := inv.operands()
	if len(operands) == 0 {
		return inv.usage("file", "at least one PATH")
	}
	brief, mime := inv.flag("-b"), inv.flag("-i")
	for _, operand := range operands {
		id, ok, err := inv.identifyOperand(operand)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		answer := id.description
		if mime {
			charset := id.charset
			if charset == "" {
				charset = "binary"
			}
			answer = id.mime + "; charset=" + charset
		}
		if !brief {
			answer = operand + ": " + answer
		}
		if err := inv.out.text(answer + "\n"); err != nil {
			return err
		}
	}
	return nil
}

// identifyOperand reports links and directories from metadata and reads
// content only for other entries.
func (inv *invocation) identifyOperand(operand string) (identification, bool, error) {
	if operand != "-" {
		st, ok, err := inv.entry(operand, false)
		if err != nil || !ok {
			return identification{}, ok, err
		}
		switch typeLetter(st.Mode) {
		case 'd':
			return identification{description: "directory", mime: "inode/directory"}, true, nil
		case 'l':
			bound, err := inv.paths.Bind(inv.ctx, operand, false)
			if err != nil {
				return identification{}, false, err
			}
			target, err := bound.Readlink()
			if err != nil {
				return identification{}, false, err
			}
			return identification{description: "symbolic link to " + target, mime: "inode/symlink"}, true, nil
		}
	}
	in, err := inv.open(operand, true)
	if err != nil || in == nil {
		return identification{}, false, err
	}
	return inv.owners.signatures.identify(in.data), true, nil
}
