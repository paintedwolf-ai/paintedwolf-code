package pythonruntime

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	// FuelGlobalExportName is the exported mutable i64 global injected by
	// InstrumentWasmFuel for deterministic per-invocation guest work metering.
	FuelGlobalExportName = "__pw_fuel"
)

// InstrumentWasmFuel rewrites a WebAssembly binary to inject a deterministic
// mutable i64 fuel global (__pw_fuel) checked at every function entry and
// immediately after every loop header. When fuel drops below zero, the guest
// executes unreachable, terminating deterministically without wall-clock
// dependence. Existing function, table, memory, and global indices are
// preserved.
func InstrumentWasmFuel(wasmBytes []byte) ([]byte, error) {
	if len(wasmBytes) < 8 || !bytes.Equal(wasmBytes[:4], []byte{0x00, 0x61, 0x73, 0x6d}) {
		return nil, fmt.Errorf("instrument wasm fuel: invalid wasm header")
	}

	type section struct {
		id      byte
		payload []byte
	}
	var sections []section
	pos := 8
	for pos < len(wasmBytes) {
		secID := wasmBytes[pos]
		pos++
		secLen, n, err := readULEB128(wasmBytes[pos:])
		if err != nil {
			return nil, fmt.Errorf("instrument wasm fuel: section %d length: %w", secID, err)
		}
		pos += n
		if pos+int(secLen) > len(wasmBytes) {
			return nil, fmt.Errorf("instrument wasm fuel: section %d truncated", secID)
		}
		payload := wasmBytes[pos : pos+int(secLen)]
		pos += int(secLen)
		// Drop custom sections (e.g. DWARF debug info) in instrumented copy.
		if secID == 0 {
			continue
		}
		sections = append(sections, section{id: secID, payload: payload})
	}

	// Count imported globals (section 2) and defined globals (section 6).
	var importedGlobals uint32
	var definedGlobals uint32
	hasGlobalSec := false
	hasExportSec := false
	for _, s := range sections {
		switch s.id {
		case 2:
			cnt, err := countImportedGlobals(s.payload)
			if err != nil {
				return nil, fmt.Errorf("instrument wasm fuel: import section: %w", err)
			}
			importedGlobals = cnt
		case 6:
			hasGlobalSec = true
			cnt, _, err := readULEB128(s.payload)
			if err != nil {
				return nil, fmt.Errorf("instrument wasm fuel: global section: %w", err)
			}
			definedGlobals = cnt
		case 7:
			hasExportSec = true
		}
	}
	fuelGlobalIdx := importedGlobals + definedGlobals
	fuelCheck := buildFuelCheckBytes(fuelGlobalIdx)

	// New mutable i64 global initialized to 0: valtype=0x7e, mut=0x01, i64.const 0, end
	newGlobalEntry := []byte{0x7e, 0x01, 0x42, 0x00, 0x0b}
	// New export entry: "__pw_fuel", kind=0x03 (global), idx=fuelGlobalIdx
	var newExportEntry bytes.Buffer
	writeULEB128(&newExportEntry, uint32(len(FuelGlobalExportName)))
	newExportEntry.WriteString(FuelGlobalExportName)
	newExportEntry.WriteByte(0x03)
	writeULEB128(&newExportEntry, fuelGlobalIdx)

	var out bytes.Buffer
	out.Grow(len(wasmBytes) + len(wasmBytes)/8)
	out.Write(wasmBytes[:8])

	insertedGlobal := hasGlobalSec
	insertedExport := hasExportSec

	emitGlobalSec := func() {
		var p bytes.Buffer
		writeULEB128(&p, 1)
		p.Write(newGlobalEntry)
		writeWasmSection(&out, 6, p.Bytes())
		insertedGlobal = true
	}
	emitExportSec := func() {
		var p bytes.Buffer
		writeULEB128(&p, 1)
		p.Write(newExportEntry.Bytes())
		writeWasmSection(&out, 7, p.Bytes())
		insertedExport = true
	}

	for _, s := range sections {
		if !insertedGlobal && s.id > 6 {
			emitGlobalSec()
		}
		if !insertedExport && s.id > 7 {
			emitExportSec()
		}
		switch s.id {
		case 6:
			cnt, n, err := readULEB128(s.payload)
			if err != nil {
				return nil, err
			}
			var p bytes.Buffer
			writeULEB128(&p, cnt+1)
			p.Write(s.payload[n:])
			p.Write(newGlobalEntry)
			writeWasmSection(&out, 6, p.Bytes())
		case 7:
			cnt, n, err := readULEB128(s.payload)
			if err != nil {
				return nil, err
			}
			var p bytes.Buffer
			writeULEB128(&p, cnt+1)
			p.Write(s.payload[n:])
			p.Write(newExportEntry.Bytes())
			writeWasmSection(&out, 7, p.Bytes())
		case 10:
			instrumentedCode, err := instrumentCodeSection(s.payload, fuelCheck)
			if err != nil {
				return nil, fmt.Errorf("instrument wasm fuel: code section: %w", err)
			}
			writeWasmSection(&out, 10, instrumentedCode)
		default:
			writeWasmSection(&out, s.id, s.payload)
		}
	}
	if !insertedGlobal {
		emitGlobalSec()
	}
	if !insertedExport {
		emitExportSec()
	}

	return out.Bytes(), nil
}

// buildFuelCheckBytes encodes:
//
//	global.get $fuel
//	i64.const 1
//	i64.sub
//	global.set $fuel
//	global.get $fuel
//	i64.const 0
//	i64.lt_s
//	if (void)
//	  unreachable
//	end
func buildFuelCheckBytes(fuelGlobalIdx uint32) []byte {
	var b bytes.Buffer
	b.WriteByte(0x23) // global.get
	writeULEB128(&b, fuelGlobalIdx)
	b.Write([]byte{0x42, 0x01}) // i64.const 1
	b.WriteByte(0x7d)           // i64.sub
	b.WriteByte(0x24)           // global.set
	writeULEB128(&b, fuelGlobalIdx)
	b.WriteByte(0x23) // global.get
	writeULEB128(&b, fuelGlobalIdx)
	b.Write([]byte{0x42, 0x00}) // i64.const 0
	b.WriteByte(0x53)           // i64.lt_s
	b.Write([]byte{0x04, 0x40}) // if (void)
	b.WriteByte(0x00)           // unreachable
	b.WriteByte(0x0b)           // end
	return b.Bytes()
}

func countImportedGlobals(payload []byte) (uint32, error) {
	cnt, pos, err := readULEB128(payload)
	if err != nil {
		return 0, err
	}
	var globals uint32
	for i := uint32(0); i < cnt; i++ {
		// module name
		mlen, n, err := readULEB128(payload[pos:])
		if err != nil {
			return 0, err
		}
		pos += n + int(mlen)
		// field name
		flen, n, err := readULEB128(payload[pos:])
		if err != nil {
			return 0, err
		}
		pos += n + int(flen)
		if pos >= len(payload) {
			return 0, fmt.Errorf("truncated import kind")
		}
		kind := payload[pos]
		pos++
		switch kind {
		case 0: // func
			_, n, err = readULEB128(payload[pos:])
			if err != nil {
				return 0, err
			}
			pos += n
		case 1: // table: reftype + limits
			pos++
			n, err = skipLimits(payload[pos:])
			if err != nil {
				return 0, err
			}
			pos += n
		case 2: // memory: limits
			n, err = skipLimits(payload[pos:])
			if err != nil {
				return 0, err
			}
			pos += n
		case 3: // global: valtype + mut
			globals++
			pos += 2
		default:
			return 0, fmt.Errorf("unsupported import kind %d", kind)
		}
	}
	return globals, nil
}

func skipLimits(b []byte) (int, error) {
	flags, pos, err := readULEB128(b)
	if err != nil {
		return 0, err
	}
	_, n, err := readULEB128(b[pos:])
	if err != nil {
		return 0, err
	}
	pos += n
	if flags&1 != 0 {
		_, n, err = readULEB128(b[pos:])
		if err != nil {
			return 0, err
		}
		pos += n
	}
	return pos, nil
}

func instrumentCodeSection(payload, fuelCheck []byte) ([]byte, error) {
	count, pos, err := readULEB128(payload)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Grow(len(payload) + len(payload)/4)
	writeULEB128(&out, count)

	for i := uint32(0); i < count; i++ {
		bodyLen, n, err := readULEB128(payload[pos:])
		if err != nil {
			return nil, fmt.Errorf("func %d length: %w", i, err)
		}
		pos += n
		if pos+int(bodyLen) > len(payload) {
			return nil, fmt.Errorf("func %d truncated", i)
		}
		body := payload[pos : pos+int(bodyLen)]
		pos += int(bodyLen)

		newBody, err := instrumentFunctionBody(body, fuelCheck)
		if err != nil {
			return nil, fmt.Errorf("func %d: %w", i, err)
		}
		writeULEB128(&out, uint32(len(newBody)))
		out.Write(newBody)
	}
	return out.Bytes(), nil
}

func instrumentFunctionBody(body, fuelCheck []byte) ([]byte, error) {
	localDeclCount, pos, err := readULEB128(body)
	if err != nil {
		return nil, err
	}
	for i := uint32(0); i < localDeclCount; i++ {
		_, n, err := readULEB128(body[pos:])
		if err != nil {
			return nil, err
		}
		pos += n
		if pos >= len(body) {
			return nil, fmt.Errorf("truncated local decl")
		}
		pos++ // valtype
	}

	var out bytes.Buffer
	out.Grow(len(body) + len(fuelCheck)*4)
	// Preserve local declarations verbatim.
	out.Write(body[:pos])
	// Inject fuel check at function entry.
	out.Write(fuelCheck)

	for pos < len(body) {
		op := body[pos]
		start := pos
		pos++

		immLen, err := immediateLength(op, body[pos:])
		if err != nil {
			return nil, fmt.Errorf("offset %d opcode 0x%02x: %w", start, op, err)
		}
		if pos+immLen > len(body) {
			return nil, fmt.Errorf("offset %d opcode 0x%02x truncated immediate", start, op)
		}
		pos += immLen
		out.Write(body[start:pos])

		// Inject fuel check immediately after every loop header (every backward branch targets a loop).
		if op == 0x03 {
			out.Write(fuelCheck)
		}
	}
	return out.Bytes(), nil
}

func immediateLength(op byte, rest []byte) (int, error) {
	switch op {
	case 0x02, 0x03, 0x04: // block, loop, if: blocktype
		if len(rest) == 0 {
			return 0, fmt.Errorf("missing blocktype")
		}
		b := rest[0]
		if b == 0x40 || b == 0x7f || b == 0x7e || b == 0x7d || b == 0x7c || b == 0x7b || b == 0x70 || b == 0x6f {
			return 1, nil
		}
		_, n, err := readSLEB128(rest)
		return n, err
	case 0x0c, 0x0d, 0x10, 0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0xd2:
		_, n, err := readULEB128(rest)
		return n, err
	case 0x0e: // br_table: vec(labelidx) + labelidx
		cnt, pos, err := readULEB128(rest)
		if err != nil {
			return 0, err
		}
		for i := uint32(0); i <= cnt; i++ {
			_, n, err := readULEB128(rest[pos:])
			if err != nil {
				return 0, err
			}
			pos += n
		}
		return pos, nil
	case 0x11: // call_indirect: typeidx, tableidx
		_, n1, err := readULEB128(rest)
		if err != nil {
			return 0, err
		}
		_, n2, err := readULEB128(rest[n1:])
		return n1 + n2, err
	case 0x1c: // typed select: vec(valtype)
		cnt, pos, err := readULEB128(rest)
		if err != nil {
			return 0, err
		}
		return pos + int(cnt), nil
	case 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f,
		0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37,
		0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, 0x3e: // memarg (align, offset)
		_, n1, err := readULEB128(rest)
		if err != nil {
			return 0, err
		}
		_, n2, err := readULEB128(rest[n1:])
		return n1 + n2, err
	case 0x3f, 0x40: // memory.size, memory.grow
		_, n, err := readULEB128(rest)
		return n, err
	case 0x41, 0x42: // i32.const, i64.const
		_, n, err := readSLEB128(rest)
		return n, err
	case 0x43: // f32.const
		return 4, nil
	case 0x44: // f64.const
		return 8, nil
	case 0xd0: // ref.null
		return 1, nil
	case 0xfc: // saturating trunc + bulk memory + table ops
		sub, pos, err := readULEB128(rest)
		if err != nil {
			return 0, err
		}
		switch sub {
		case 0, 1, 2, 3, 4, 5, 6, 7:
			return pos, nil
		case 8, 10, 12, 14: // two uleb128 immediates
			_, n1, err := readULEB128(rest[pos:])
			if err != nil {
				return 0, err
			}
			_, n2, err := readULEB128(rest[pos+n1:])
			return pos + n1 + n2, err
		case 9, 11, 13, 15, 16, 17: // one uleb128 immediate
			_, n1, err := readULEB128(rest[pos:])
			return pos + n1, err
		default:
			return 0, fmt.Errorf("unsupported 0xfc sub-opcode %d", sub)
		}
	default:
		// All other standard single-byte Wasm opcodes carry no immediate.
		return 0, nil
	}
}

func writeWasmSection(out *bytes.Buffer, id byte, payload []byte) {
	out.WriteByte(id)
	writeULEB128(out, uint32(len(payload)))
	out.Write(payload)
}

func writeULEB128(out *bytes.Buffer, v uint32) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out.WriteByte(b)
		if v == 0 {
			return
		}
	}
}

func readULEB128(b []byte) (uint32, int, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, 0, fmt.Errorf("invalid uleb128")
	}
	return uint32(v), n, nil
}

func readSLEB128(b []byte) (int64, int, error) {
	var result int64
	var shift uint
	for i := 0; i < len(b) && i < 10; i++ {
		by := b[i]
		result |= int64(by&0x7f) << shift
		shift += 7
		if by&0x80 == 0 {
			if shift < 64 && by&0x40 != 0 {
				result |= ^int64(0) << shift
			}
			return result, i + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("invalid sleb128")
}
