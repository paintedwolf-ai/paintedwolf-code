// Package onefile reads and rewrites the selected Linux Nuitka payload grammar.
package onefile

import (
	"bufio"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
)

const MaxBytes = 512 << 20
const maxMembers = 16384

type Member struct {
	Name       string
	Executable bool
	Link       string
	Data       []byte
}

type Image struct {
	Launcher []byte
	Members  []Member
}

// Read accepts the compressed, checksummed Linux grammar used by Nuitka 2.8.9.
func Read(raw []byte) (*Image, error) {
	if len(raw) < 75 || len(raw) > MaxBytes {
		return nil, fmt.Errorf("onefile image exceeds its size bounds")
	}
	size := binary.LittleEndian.Uint64(raw[len(raw)-8:])
	if size < 7 || size > uint64(len(raw)-72) {
		return nil, fmt.Errorf("onefile payload length is invalid")
	}
	start := len(raw) - 8 - int(size)
	if !bytes.Equal(raw[start:start+7], []byte{'K', 'A', 'Y', 0x28, 0xb5, 0x2f, 0xfd}) {
		return nil, fmt.Errorf("onefile compressed payload header is invalid")
	}
	if err := validateLauncher(raw[:start]); err != nil {
		return nil, err
	}
	decoder, err := zstd.NewReader(bytes.NewReader(raw[start+3:len(raw)-8]), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20), zstd.WithDecoderMaxWindow(128<<20))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	bounded := &io.LimitedReader{R: decoder, N: MaxBytes + 1}
	reader := bufio.NewReaderSize(bounded, 1025)
	image := &Image{Launcher: append([]byte(nil), raw[:start]...)}
	seen := make(map[string]bool)
	total := 0
	for {
		name, err := readName(reader)
		if err != nil {
			return nil, err
		}
		if name == "" {
			if _, err := reader.ReadByte(); err != io.EOF || bounded.N == 0 || len(image.Members) == 0 {
				return nil, fmt.Errorf("onefile payload has trailing data or no members")
			}
			return image, nil
		}
		if !validName(name) || seen[name] || len(image.Members) >= maxMembers {
			return nil, fmt.Errorf("onefile member name is invalid or repeated")
		}
		seen[name] = true
		flags, err := reader.ReadByte()
		if err != nil || flags > 2 {
			return nil, fmt.Errorf("onefile member flags are invalid")
		}
		member := Member{Name: name, Executable: flags == 1}
		if flags == 2 {
			member.Link, err = readName(reader)
			if err != nil || !validLink(name, member.Link) {
				return nil, fmt.Errorf("onefile symlink escapes its payload")
			}
		} else {
			var size uint64
			var checksum uint32
			if err := binary.Read(reader, binary.LittleEndian, &size); err != nil {
				return nil, err
			}
			if err := binary.Read(reader, binary.LittleEndian, &checksum); err != nil {
				return nil, err
			}
			if size > MaxBytes || uint64(total)+size > MaxBytes {
				return nil, fmt.Errorf("onefile expanded payload exceeds its bound")
			}
			total += int(size)
			member.Data = make([]byte, int(size))
			if _, err := io.ReadFull(reader, member.Data); err != nil {
				return nil, err
			}
			if checksum != dataChecksum(member.Data) {
				return nil, fmt.Errorf("onefile member checksum differs: %s", name)
			}
		}
		image.Members = append(image.Members, member)
	}
}

func readName(reader *bufio.Reader) (string, error) {
	raw, err := reader.ReadSlice(0)
	if err != nil {
		return "", fmt.Errorf("read onefile name: %w", err)
	}
	if len(raw) > 1024 || !utf8.Valid(raw[:len(raw)-1]) {
		return "", fmt.Errorf("onefile name is unterminated or exceeds its bound")
	}
	return string(raw[:len(raw)-1]), nil
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && utf8.ValidString(name) && path.Clean(name) == name && !path.IsAbs(name) && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00")
}

func validLink(name, target string) bool {
	resolved := path.Join(path.Dir(name), target)
	return target != "" && utf8.ValidString(target) && !path.IsAbs(target) && !strings.ContainsAny(target, "\\\x00") && validName(resolved)
}

func dataChecksum(raw []byte) uint32 {
	checksum := crc32.ChecksumIEEE(raw)
	if checksum == 0 {
		return 1
	}
	return checksum
}

func validateLauncher(raw []byte) error {
	if len(raw) < 64 || raw[4] != 2 || raw[5] != 1 {
		return fmt.Errorf("onefile launcher requires a bounded ELF64 header")
	}
	programs, sections := binary.LittleEndian.Uint16(raw[56:]), binary.LittleEndian.Uint16(raw[60:])
	programOffset, sectionOffset := binary.LittleEndian.Uint64(raw[32:]), binary.LittleEndian.Uint64(raw[40:])
	if programs > 4096 || sections > 4096 || (sections == 0 && sectionOffset != 0) || binary.LittleEndian.Uint16(raw[62:]) == 0xffff {
		return fmt.Errorf("onefile launcher has unsupported extended ELF tables")
	}
	if programs != 0 && (binary.LittleEndian.Uint16(raw[54:]) != 56 || programOffset > uint64(len(raw)) || uint64(programs) > (uint64(len(raw))-programOffset)/56) {
		return fmt.Errorf("onefile launcher program table overlaps attached payload")
	}
	if sections != 0 && (binary.LittleEndian.Uint16(raw[58:]) != 64 || sectionOffset > uint64(len(raw)) || uint64(sections) > (uint64(len(raw))-sectionOffset)/64) {
		return fmt.Errorf("onefile launcher section table overlaps attached payload")
	}
	image, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer func() { _ = image.Close() }()
	if image.Class != elf.ELFCLASS64 || image.Data != elf.ELFDATA2LSB || (image.Machine != elf.EM_X86_64 && image.Machine != elf.EM_AARCH64) {
		return fmt.Errorf("onefile launcher requires supported little-endian Linux ELF")
	}
	for _, program := range image.Progs {
		if program.Off > uint64(len(raw)) || program.Filesz > uint64(len(raw))-program.Off {
			return fmt.Errorf("onefile launcher segment overlaps attached payload")
		}
	}
	for _, section := range image.Sections {
		if section.Type != elf.SHT_NOBITS && (section.Offset > uint64(len(raw)) || section.Size > uint64(len(raw))-section.Offset) {
			return fmt.Errorf("onefile launcher section overlaps attached payload")
		}
	}
	return nil
}
