"""Read the1433 terminal diagnostic from the captured native stderr, not error prose."""
import time
from ..installed_input.mailbox import decode
from .records import CaseFailed, CaseUnavailable
from ..installed_process import process_identity

PREFIX = b'Native startup outcome: '
FIELDS = {'format_version', 'protocol', 'pid', 'sequence', 'kind', 'phase', 'elapsed_ms', 'code'}


def failed_rows(raw):
    if not isinstance(raw, bytes) or len(raw) > 8 << 20:
        raise ValueError('native startup stream exceeds existing capture bound')
    result = []
    for line in raw.splitlines(keepends=True):
        if not line.endswith(b'\n') or not line.startswith(PREFIX): continue
        if len(line) > 2048: raise ValueError('native startup metadata exceeds its bound')
        row = decode(line[len(PREFIX):])
        if (set(row) != FIELDS or type(row['format_version']) is not int or row['format_version'] != 1
                or type(row['protocol']) is not int or row['protocol'] != 1
                or type(row['pid']) is not int or not 1 < row['pid'] <= 0xffffffff
                or type(row['sequence']) is not int or not 0 < row['sequence'] <= 0xffffffffffffffff
                or type(row['elapsed_ms']) is not int or not 0 <= row['elapsed_ms'] <= 0xffffffffffffffff
                or row['kind'] != 'failed' or row['phase'] != 'credentials'
                or row['code'] != 'credential_vault_custodian_absent'):
            raise ValueError('native startup metadata differs from the exact absence contract')
        result.append(row)
    if len(result) > 1: raise ValueError('native startup terminal outcome is ambiguous')
    return result


def await_absence(app, reservation):
    started = time.monotonic()
    deadline = min(reservation.deadline, started + app.timeout)
    while time.monotonic() < deadline:
        reservation.check_launch(app)
        app.require_running()
        if app.try_ready():
            raise CaseFailed('custodian-absent launch unexpectedly became ready')
        native = app.observations['launch_identity']
        if process_identity(native['pid']) != native:
            raise CaseFailed('selected native startup capture changed its kernel identity')
        rows = failed_rows(app.streams()['app-stderr'])
        if process_identity(native['pid']) != native:
            raise CaseFailed('selected native startup capture changed while reading')
        if rows:
            return {'native_terminal': rows[0], 'launch_identity': app.observations['launch_identity'],
                    'elapsed_observation_seconds': time.monotonic() - started,
                    'child_pid_validated_by_native_startup_owner': True,
                    'pid_is_kernel_birth': False, 'health_absence_is_failure_proof': False}
        time.sleep(0.05)
    raise CaseUnavailable('typed-custodian-absent-startup-outcome-unverified')
