package pythonruntime

import (
	"context"
	"encoding/binary"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
)

// WASI preview1 errno constants (subset used by CPython/WASI).
const (
	wasiErrnoSuccess  uint32 = 0
	wasiErrnoAcces    uint32 = 2
	wasiErrnoAgain    uint32 = 6
	wasiErrnoBadf     uint32 = 8
	wasiErrnoExist    uint32 = 20
	wasiErrnoFault    uint32 = 21
	wasiErrnoInval    uint32 = 28
	wasiErrnoIO       uint32 = 29
	wasiErrnoIsdir    uint32 = 31
	wasiErrnoNoent    uint32 = 44
	wasiErrnoNosys    uint32 = 52
	wasiErrnoNotdir   uint32 = 54
	wasiErrnoNotempty uint32 = 55
	wasiErrnoNotsup   uint32 = 58
	wasiErrnoPerm     uint32 = 63
	wasiErrnoRofs     uint32 = 69
	wasiErrnoSpipe    uint32 = 70
)

// WASI preview1 filetype constants.
const (
	wasiFiletypeUnknown          byte = 0
	wasiFiletypeCharacterDevice  byte = 2
	wasiFiletypeDirectory        byte = 3
	wasiFiletypeRegularFile      byte = 4
	wasiFiletypeSymbolicLink     byte = 7
)

// WASI preview1 open flags (oflags) and fd flags.
const (
	wasiOflagCreat     uint32 = 1 << 0
	wasiOflagDirectory uint32 = 1 << 1
	wasiOflagExcl      uint32 = 1 << 2
	wasiOflagTrunc     uint32 = 1 << 3

	wasiFdflagAppend uint32 = 1 << 0

	wasiRightFdRead  uint64 = 1 << 1
	wasiRightFdWrite uint64 = 1 << 6
)

// WASI op IDs matching pw_py_wasi_op_t in pw_py_engine.h (0..42).
const (
	wasiOpArgsGet             uint32 = 0
	wasiOpArgsSizesGet        uint32 = 1
	wasiOpClockResGet         uint32 = 2
	wasiOpClockTimeGet        uint32 = 3
	wasiOpEnvironGet          uint32 = 4
	wasiOpEnvironSizesGet     uint32 = 5
	wasiOpFdAdvise            uint32 = 6
	wasiOpFdAllocate          uint32 = 7
	wasiOpFdClose             uint32 = 8
	wasiOpFdDatasync          uint32 = 9
	wasiOpFdFdstatGet         uint32 = 10
	wasiOpFdFdstatSetFlags    uint32 = 11
	wasiOpFdFilestatGet       uint32 = 12
	wasiOpFdFilestatSetSize   uint32 = 13
	wasiOpFdFilestatSetTimes  uint32 = 14
	wasiOpFdPread             uint32 = 15
	wasiOpFdPrestatDirName    uint32 = 16
	wasiOpFdPrestatGet        uint32 = 17
	wasiOpFdPwrite            uint32 = 18
	wasiOpFdRead              uint32 = 19
	wasiOpFdReaddir           uint32 = 20
	wasiOpFdSeek              uint32 = 21
	wasiOpFdSync              uint32 = 22
	wasiOpFdTell              uint32 = 23
	wasiOpFdWrite             uint32 = 24
	wasiOpPathCreateDirectory uint32 = 25
	wasiOpPathFilestatGet     uint32 = 26
	wasiOpPathFilestatSetTime uint32 = 27
	wasiOpPathLink            uint32 = 28
	wasiOpPathOpen            uint32 = 29
	wasiOpPathReadlink        uint32 = 30
	wasiOpPathRemoveDirectory uint32 = 31
	wasiOpPathRename          uint32 = 32
	wasiOpPathSymlink         uint32 = 33
	wasiOpPathUnlinkFile      uint32 = 34
	wasiOpPollOneoff          uint32 = 35
	wasiOpProcExit            uint32 = 36
	wasiOpRandomGet           uint32 = 37
	wasiOpSchedYield          uint32 = 38
	wasiOpSockAccept          uint32 = 39
	wasiOpSockRecv            uint32 = 40
	wasiOpSockSend            uint32 = 41
	wasiOpSockShutdown        uint32 = 42
)

// WASI callback actions matching pw_py_wasi_action_t.
const (
	wasiActionContinue uint32 = 0
	wasiActionExit     uint32 = 1
	wasiActionHostStop uint32 = 2
	wasiActionTrap     uint32 = 3
)

type nativeFD struct {
	isPreopen  bool
	mountName  string
	fs         experimentalsys.FS
	relPath    string
	file       experimentalsys.File
	isDir      bool
	fdflags    uint16
	dirEntries []experimentalsys.Dirent
	dirLoaded  bool
}

type nativePreopen struct {
	guestPath string
	fs        experimentalsys.FS
}

// nativeWASIHost dispatches all 43 wasi_snapshot_preview1 imports from the
// native AOT engine directly onto Go's existing WorkspaceSysFS, ExtraSysMounts,
// read-only image mounts, Clock, Entropy, StopLatch, and WorkMeter.
type nativeWASIHost struct {
	ctx      context.Context
	args     []string
	env      []string
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	clock    Clock
	entropy  Entropy
	latch    *StopLatch
	meter    *WorkMeter
	procExit bool
	exitCode uint32

	mu     sync.Mutex
	fds    map[uint32]*nativeFD
	nextFD uint32
}

func newNativeWASIHost(
	ctx context.Context,
	args []string,
	envMap map[string]string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	clock Clock,
	entropy Entropy,
	latch *StopLatch,
	meter *WorkMeter,
	preopens []nativePreopen,
) *nativeWASIHost {
	envKeys := make([]string, 0, len(envMap))
	for k := range envMap {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	env := make([]string, 0, len(envKeys))
	for _, k := range envKeys {
		env = append(env, k+"="+envMap[k])
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	fds := make(map[uint32]*nativeFD, len(preopens)+8)
	var fd uint32 = 3
	for _, p := range preopens {
		fds[fd] = &nativeFD{
			isPreopen: true,
			mountName: p.guestPath,
			fs:        p.fs,
			relPath:   ".",
			isDir:     true,
		}
		fd++
	}
	return &nativeWASIHost{
		ctx:     ctx,
		args:    args,
		env:     env,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		clock:   clock,
		entropy: entropy,
		latch:   latch,
		meter:   meter,
		fds:     fds,
		nextFD:  fd,
	}
}

func (h *nativeWASIHost) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for fd, entry := range h.fds {
		if entry != nil && entry.file != nil {
			_ = entry.file.Close()
		}
		delete(h.fds, fd)
	}
}

func (h *nativeWASIHost) dispatch(mem []byte, opID uint32, args []uint64) (action uint32, ret uint32) {
	if h.latch != nil && h.latch.Stopped() {
		return wasiActionHostStop, wasiErrnoAcces
	}
	if h.ctx.Err() != nil {
		return wasiActionHostStop, wasiErrnoAcces
	}

	switch opID {
	case wasiOpProcExit:
		code := uint32(0)
		if len(args) > 0 {
			code = uint32(args[0])
		}
		h.procExit = true
		h.exitCode = code
		return wasiActionExit, code

	case wasiOpArgsSizesGet:
		return wasiActionContinue, h.handleStringVecSizes(mem, h.args, args)
	case wasiOpArgsGet:
		return wasiActionContinue, h.handleStringVecGet(mem, h.args, args)
	case wasiOpEnvironSizesGet:
		return wasiActionContinue, h.handleStringVecSizes(mem, h.env, args)
	case wasiOpEnvironGet:
		return wasiActionContinue, h.handleStringVecGet(mem, h.env, args)

	case wasiOpClockResGet:
		return wasiActionContinue, h.handleClockResGet(mem, args)
	case wasiOpClockTimeGet:
		return wasiActionContinue, h.handleClockTimeGet(mem, args)
	case wasiOpRandomGet:
		return wasiActionContinue, h.handleRandomGet(mem, args)
	case wasiOpSchedYield:
		return wasiActionContinue, wasiErrnoSuccess
	case wasiOpPollOneoff:
		return wasiActionContinue, h.handlePollOneoff(mem, args)

	case wasiOpFdPrestatGet:
		return wasiActionContinue, h.handleFdPrestatGet(mem, args)
	case wasiOpFdPrestatDirName:
		return wasiActionContinue, h.handleFdPrestatDirName(mem, args)
	case wasiOpFdFdstatGet:
		return wasiActionContinue, h.handleFdFdstatGet(mem, args)
	case wasiOpFdFdstatSetFlags:
		return wasiActionContinue, h.handleFdFdstatSetFlags(args)
	case wasiOpFdFilestatGet:
		return wasiActionContinue, h.handleFdFilestatGet(mem, args)
	case wasiOpFdFilestatSetSize:
		return wasiActionContinue, h.handleFdFilestatSetSize(args)
	case wasiOpFdFilestatSetTimes:
		return wasiActionContinue, h.handleFdFilestatSetTimes(args)
	case wasiOpFdAdvise:
		return wasiActionContinue, wasiErrnoSuccess
	case wasiOpFdAllocate:
		return wasiActionContinue, wasiErrnoNotsup
	case wasiOpFdClose:
		return wasiActionContinue, h.handleFdClose(args)
	case wasiOpFdDatasync:
		return wasiActionContinue, h.handleFdSync(args, true)
	case wasiOpFdSync:
		return wasiActionContinue, h.handleFdSync(args, false)
	case wasiOpFdRead:
		return h.postCheck(h.handleFdRead(mem, args))
	case wasiOpFdPread:
		return h.postCheck(h.handleFdPread(mem, args))
	case wasiOpFdWrite:
		return h.postCheck(h.handleFdWrite(mem, args))
	case wasiOpFdPwrite:
		return h.postCheck(h.handleFdPwrite(mem, args))
	case wasiOpFdSeek:
		return wasiActionContinue, h.handleFdSeek(mem, args)
	case wasiOpFdTell:
		return wasiActionContinue, h.handleFdTell(mem, args)
	case wasiOpFdReaddir:
		return h.postCheck(h.handleFdReaddir(mem, args))

	case wasiOpPathOpen:
		return h.postCheck(h.handlePathOpen(mem, args))
	case wasiOpPathFilestatGet:
		return h.postCheck(h.handlePathFilestatGet(mem, args))
	case wasiOpPathFilestatSetTime:
		return h.postCheck(h.handlePathFilestatSetTimes(mem, args))
	case wasiOpPathCreateDirectory:
		return h.postCheck(h.handlePathSimple(mem, args, "mkdir"))
	case wasiOpPathRemoveDirectory:
		return h.postCheck(h.handlePathSimple(mem, args, "rmdir"))
	case wasiOpPathUnlinkFile:
		return h.postCheck(h.handlePathSimple(mem, args, "unlink"))
	case wasiOpPathRename:
		return h.postCheck(h.handlePathRename(mem, args))
	case wasiOpPathReadlink:
		return h.postCheck(h.handlePathReadlink(mem, args))
	case wasiOpPathLink:
		return h.postCheck(h.handlePathLink(mem, args))
	case wasiOpPathSymlink:
		return h.postCheck(h.handlePathSymlink(mem, args))

	case wasiOpSockAccept, wasiOpSockRecv, wasiOpSockSend, wasiOpSockShutdown:
		return wasiActionContinue, wasiErrnoNosys
	default:
		return wasiActionContinue, wasiErrnoNosys
	}
}

func (h *nativeWASIHost) postCheck(errno uint32) (uint32, uint32) {
	if h.latch != nil && h.latch.Stopped() {
		return wasiActionHostStop, errno
	}
	return wasiActionContinue, errno
}

func memSlice(mem []byte, ptr, length uint32) ([]byte, bool) {
	start := uint64(ptr)
	end := start + uint64(length)
	if end > uint64(len(mem)) {
		return nil, false
	}
	return mem[start:end], true
}

func putU32(mem []byte, ptr, val uint32) bool {
	s, ok := memSlice(mem, ptr, 4)
	if !ok {
		return false
	}
	binary.LittleEndian.PutUint32(s, val)
	return true
}

func putU64(mem []byte, ptr uint32, val uint64) bool {
	s, ok := memSlice(mem, ptr, 8)
	if !ok {
		return false
	}
	binary.LittleEndian.PutUint64(s, val)
	return true
}

func (h *nativeWASIHost) handleStringVecSizes(mem []byte, items []string, args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	var total uint32
	for _, s := range items {
		total += uint32(len(s) + 1)
	}
	if !putU32(mem, uint32(args[0]), uint32(len(items))) || !putU32(mem, uint32(args[1]), total) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleStringVecGet(mem []byte, items []string, args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	argvPtr := uint32(args[0])
	bufPtr := uint32(args[1])
	for _, s := range items {
		if !putU32(mem, argvPtr, bufPtr) {
			return wasiErrnoFault
		}
		argvPtr += 4
		dst, ok := memSlice(mem, bufPtr, uint32(len(s)+1))
		if !ok {
			return wasiErrnoFault
		}
		copy(dst, s)
		dst[len(s)] = 0
		bufPtr += uint32(len(s) + 1)
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleClockResGet(mem []byte, args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	clockID := uint32(args[0])
	resPtr := uint32(args[1])
	var res uint64
	switch clockID {
	case 0: // REALTIME
		res = uint64(h.clock.WallResolutionNanos())
	case 1, 2, 3: // MONOTONIC / PROCESS_CPUTIME / THREAD_CPUTIME
		res = uint64(h.clock.MonotonicResolutionNanos())
	default:
		return wasiErrnoInval
	}
	if !putU64(mem, resPtr, res) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleClockTimeGet(mem []byte, args []uint64) uint32 {
	if len(args) < 3 {
		return wasiErrnoInval
	}
	clockID := uint32(args[0])
	outPtr := uint32(args[2])
	var ts uint64
	switch clockID {
	case 0: // REALTIME
		ts = uint64(h.clock.Now().UnixNano())
	case 1, 2, 3: // MONOTONIC / CPUTIME
		ts = uint64(h.clock.MonotonicNanos())
	default:
		return wasiErrnoInval
	}
	if !putU64(mem, outPtr, ts) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleRandomGet(mem []byte, args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	dst, ok := memSlice(mem, uint32(args[0]), uint32(args[1]))
	if !ok {
		return wasiErrnoFault
	}
	if _, err := io.ReadFull(h.entropy.Reader(), dst); err != nil {
		return wasiErrnoIO
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handlePollOneoff(mem []byte, args []uint64) uint32 {
	if len(args) < 4 {
		return wasiErrnoInval
	}
	inPtr := uint32(args[0])
	outPtr := uint32(args[1])
	nsub := uint32(args[2])
	neventsPtr := uint32(args[3])
	if nsub == 0 {
		return wasiErrnoInval
	}
	// Each subscription is 48 bytes; each event is 32 bytes.
	subs, ok := memSlice(mem, inPtr, nsub*48)
	if !ok {
		return wasiErrnoFault
	}
	evts, ok := memSlice(mem, outPtr, nsub*32)
	if !ok {
		return wasiErrnoFault
	}
	var maxSleep time.Duration
	for i := uint32(0); i < nsub; i++ {
		sub := subs[i*48 : (i+1)*48]
		userdata := binary.LittleEndian.Uint64(sub[0:8])
		tag := sub[8]
		evt := evts[i*32 : (i+1)*32]
		clear(evt)
		binary.LittleEndian.PutUint64(evt[0:8], userdata)
		evt[10] = tag
		if tag == 0 { // clock
			timeout := binary.LittleEndian.Uint64(sub[24:32])
			flags := binary.LittleEndian.Uint16(sub[40:42])
			var d time.Duration
			if flags&1 != 0 { // ABSTIME
				now := uint64(h.clock.MonotonicNanos())
				if timeout > now {
					d = time.Duration(timeout - now)
				}
			} else {
				d = time.Duration(timeout)
			}
			if d > maxSleep {
				maxSleep = d
			}
		}
	}
	if maxSleep > 0 {
		timer := time.NewTimer(maxSleep)
		select {
		case <-timer.C:
		case <-h.ctx.Done():
			timer.Stop()
		}
	}
	if !putU32(mem, neventsPtr, nsub) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

