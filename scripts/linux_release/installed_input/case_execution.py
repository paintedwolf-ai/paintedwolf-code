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
