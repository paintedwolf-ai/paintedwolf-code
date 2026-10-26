"""Validate a managed task verdict after the caller establishes job settlement."""
import math
import re

from verification_plan import outcome_exit


def _code(value):
    return type(value) is int and 0 <= value <= 255


def _finished(value):
    return type(value) in (int, float) and value > 0 and (
        type(value) is int or math.isfinite(value))


class ManagedCaseOutcome(RuntimeError):
    def __init__(self, wait_exit, result, terminal, *, consistent):
        result = result if isinstance(result, dict) else {}
        terminal = terminal if isinstance(terminal, dict) else {}
        state = result.get('state')
        task = result.get('task_exit_code')
        code = terminal.get('exit_code')
        self.facts = {
            'type': 'ManagedCaseOutcome',
            'wait_exit_code': wait_exit if _code(wait_exit) else None,
            'task_exit_code': task if _code(task) else None,
            'terminal_exit_code': code if _code(code) else None,
            'state': state if state in ('completed', 'unverified') else 'unknown',
            'consistent': consistent,
            'outcome': 'failed' if consistent and wait_exit == 1 else 'unverified',
        }
        super().__init__('managed task did not pass; structured verdict retained')


def require_case_success(wait_exit, result, terminal):
    """Require coherent successful wait/terminal facts, without asserting quiescence.

    The caller must separately establish terminal-record authority and completion
    lock release. A settled failed or cancelled task still cannot pass this check.
    """
    consistent = False
    if isinstance(result, dict) and isinstance(terminal, dict):
        job = terminal.get('job_id')
        code = terminal.get('exit_code')
        state = terminal.get('state')
        consistent = (
            isinstance(job, str) and re.fullmatch('[a-f0-9]{32}', job) is not None
            and isinstance(terminal.get('log'), str) and bool(terminal['log'])
            and state in ('completed', 'unverified') and _code(code)
            and _finished(terminal.get('finished_at')) and _finished(result.get('finished_at'))
            and not (state == 'unverified' and code == 0)
            and all(result.get(key) == terminal.get(key) for key in ('job_id', 'log', 'state', 'finished_at'))
            and _code(result.get('task_exit_code')) and result['task_exit_code'] == code
            and _code(result.get('exit_code')) and _code(wait_exit)
            and result['exit_code'] == wait_exit == outcome_exit(code, state == 'completed')
        )
    if not consistent or wait_exit != 0:
        raise ManagedCaseOutcome(wait_exit, result, terminal, consistent=consistent)
