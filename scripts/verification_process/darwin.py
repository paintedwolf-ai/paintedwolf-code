"""Darwin verification process facts from the SDK's libproc interfaces."""
import ctypes as c
import errno
from . import ProcessObserver


class BsdInfo(c.Structure):
    _fields_ = [(name, c.c_uint32) for name in
                ('flags', 'status', 'xstatus', 'pid', 'ppid', 'uid', 'gid', 'ruid', 'rgid', 'svuid', 'svgid', 'reserved')]
    _fields_ += [('comm', c.c_char * 16), ('name', c.c_char * 32)]
    _fields_ += [(name, c.c_uint32) for name in ('nfiles', 'pgid', 'jobc', 'tdev', 'tpgid')]
    _fields_ += [('nice', c.c_int32), ('start_sec', c.c_uint64), ('start_usec', c.c_uint64)]


class FileInfo(c.Structure):
    _fields_ = [('flags', c.c_uint32), ('status', c.c_uint32), ('offset', c.c_int64),
                ('type', c.c_int32), ('guardflags', c.c_uint32)]


class VnodeStat(c.Structure):
    _fields_ = [('dev', c.c_uint32), ('mode', c.c_uint16), ('nlink', c.c_uint16),
                ('ino', c.c_uint64), ('uid', c.c_uint32), ('gid', c.c_uint32)]
    _fields_ += [(name, c.c_int64) for name in
                ('atime', 'atimensec', 'mtime', 'mtimensec', 'ctime', 'ctimensec', 'birthtime', 'birthtimensec', 'size', 'blocks')]
    _fields_ += [('blksize', c.c_int32), ('flags', c.c_uint32), ('gen', c.c_uint32),
                ('rdev', c.c_uint32), ('reserved', c.c_int64 * 2)]


class VnodeInfo(c.Structure):
    _fields_ = [('stat', VnodeStat), ('type', c.c_int32), ('pad', c.c_int32), ('fsid', c.c_int32 * 2)]


class VnodeFdInfo(c.Structure):
    _fields_ = [('file', FileInfo), ('vnode', VnodeInfo)]


class FdInfo(c.Structure):
    _fields_ = [('fd', c.c_int32), ('type', c.c_uint32)]


class Observer(ProcessObserver):
    def __init__(self):
        self.lib = c.CDLL('/usr/lib/libproc.dylib', use_errno=True)
        self.lib.proc_pidinfo.argtypes = [c.c_int, c.c_int, c.c_uint64, c.c_void_p, c.c_int]
        self.lib.proc_pidinfo.restype = c.c_int
        self.lib.proc_pidfdinfo.argtypes = [c.c_int, c.c_int, c.c_int, c.c_void_p, c.c_int]
        self.lib.proc_pidfdinfo.restype = c.c_int

    def native_birth(self, pid):
        info = BsdInfo()
        size = self.lib.proc_pidinfo(pid, 3, 0, c.byref(info), c.sizeof(info))
        if size != c.sizeof(info):
            self.fail('verification process birth')
        if info.pid != pid:
            raise ValueError('verification process birth identifies another process')
        return {'pid': pid, 'parent_pid': info.ppid, 'start_time_seconds': info.start_sec,
                'start_time_microseconds': info.start_usec, 'uid': info.uid}

    def files(self, pid, limit):
        # One extra slot distinguishes an over-bound inventory without allocating
        # memory from a count controlled by the observed process.
        inventory = (FdInfo * (limit + 1))()
        size = self.lib.proc_pidinfo(pid, 1, 0, inventory, c.sizeof(inventory))
        if size <= 0:
            self.fail('verification process descriptors')
        if size % c.sizeof(FdInfo) or size > limit * c.sizeof(FdInfo):
            raise ValueError('scheduler process descriptor inventory exceeds bound')
        rows = []
        for record in inventory[:size // c.sizeof(FdInfo)]:
            if record.type != 1:  # PROX_FDTYPE_VNODE
                continue
            info = VnodeFdInfo()
            size = self.lib.proc_pidfdinfo(pid, record.fd, 1, c.byref(info), c.sizeof(info))
            if size != c.sizeof(info):
                error = c.get_errno()
                if error in (errno.EBADF, errno.ENOENT):
                    continue
                self.fail('verification open-file identity')
            rows.append((c.c_int32(info.vnode.stat.dev).value, info.vnode.stat.ino))
        return rows

    @staticmethod
    def fail(operation):
        error = c.get_errno()
        if error == errno.ESRCH:
            raise ProcessLookupError(error, operation)
        raise OSError(error or errno.EIO, operation)
