"""Observe the selected case entry without granting input or root-effect authority."""
import json
import math
import sys
import time

from . import protocol
from .mailbox import Mailbox, decode


def observe(execution, vm_id, capability, case):
    protocol.case_execution_name(capability, case)
    box=Mailbox(execution,vm_id)
    box.marker()
    if ((not box.path.exists() and not box.path.is_symlink())
            or box.read('binding.json') is None):
        return None
    return box.read_case_execution(capability,case)


def await_execution(execution, vm_id, capability, case, deadline, read, terminal):
    protocol.binding(execution)
    protocol.case_execution_name(capability,case)
    if type(deadline) not in (int,float) or not math.isfinite(deadline):
        raise ValueError('case execution requires the existing finite deadline')
    while True:
        if time.monotonic()>=deadline:
            raise TimeoutError('case execution observation exceeded its useful lifetime')
        if terminal() is not None:
            raise ValueError('managed task terminated before case execution observation')
        fact=read(deadline)
        if time.monotonic()>=deadline:
            raise TimeoutError('case execution observation arrived after its useful lifetime')
        if terminal() is not None:
            raise ValueError('managed task terminated during case execution observation')
        if fact is not None:
            return protocol.case_execution(fact,execution,vm_id,capability,case)
        time.sleep(min(.05,max(0,deadline-time.monotonic())))


def guest():
    value=decode(sys.stdin.buffer.read(65537))
    if not isinstance(value,dict) or set(value)!={'execution','vm_id','capability','case'}:
        raise ValueError('case execution observation has an unknown request')
    fact=observe(value['execution'],value['vm_id'],value['capability'],value['case'])
    print(json.dumps({'case_execution':fact},sort_keys=True),flush=True)


def await_binding(path, deadline, terminal):
    """Read the native publisher's private atomic request before case observation."""
    import os
    from pathlib import Path
    import stat
    path=Path(path)
    if not path.is_absolute() or type(deadline) not in (int,float) or not math.isfinite(deadline):
        raise ValueError('execution request requires a physical path and finite lifetime')
    stable=lambda info:(info.st_dev,info.st_ino,info.st_mode,info.st_uid,info.st_gid,
                        info.st_nlink,info.st_size,info.st_mtime_ns,info.st_ctime_ns)
    while True:
        if time.monotonic()>=deadline or terminal() is not None:
            raise TimeoutError('native execution request not observed within its useful lifetime')
        try:
            parent=path.parent.lstat()
            if (not stat.S_ISDIR(parent.st_mode) or parent.st_uid!=os.getuid()
                    or stat.S_IMODE(parent.st_mode)!=0o700 or path.parent.resolve()!=path.parent):
                raise ValueError('execution publisher directory differs')
            descriptor=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
        except FileNotFoundError as error:
            if error.filename not in {str(path),str(path.parent)}:
                raise
            time.sleep(min(.05,max(0,deadline-time.monotonic())))
            continue
        try:
            before=os.fstat(descriptor)
            if (not stat.S_ISREG(before.st_mode) or before.st_uid!=os.getuid()
                    or stat.S_IMODE(before.st_mode)!=0o600 or before.st_nlink!=1 or before.st_size>4096):
                raise ValueError('native execution request lost its private identity')
            raw=os.read(descriptor,4097)
            if stable(before)!=stable(os.fstat(descriptor)) or stable(before)!=stable(path.lstat()):
                raise ValueError('native execution request changed during observation')
            execution=protocol.binding(decode(raw))
        finally:
            os.close(descriptor)
        if time.monotonic()>=deadline or terminal() is not None:
            raise TimeoutError('native execution request arrived outside its useful lifetime')
        return execution
