"""Run the installed confinement suite (#987) against the package's own helper.

The suite selects the installed helper by path and final SHA-256 and proves
its applied boundary through the real profile pipe. The case runs it twice:
once on the guest as it is, which must apply, and once in a transient user
unit with RestrictNamespaces=yes, where the kernel refuses the namespaces and
the helper must answer with its typed refusal. Unit-level proof: the
test:confine:linux sandbox leg (TestLinuxHelperReportsTypedUnavailability,
TestLinuxHelperRefusesWhenAttestationFails, TestClassifyPrivilegedStepNamesTheAppArmorRestriction)
and TestRequireAppliedRejectsUnavailableLinuxBoundary."""
from __future__ import annotations

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from ..campaign.runtime import unchanged

ROOT = Path(__file__).resolve().parents[3]
SUITE = ROOT / 'lycaon/test/installed/confinement'
TEST = 'TestLinuxInstalledHelperAppliedBoundary'
RECORD = 'installed_boundary='
APPARMOR_SYSCTL = Path('/proc/sys/kernel/apparmor_restrict_unprivileged_userns')
LIMIT = 4 << 20


def expected_refusal() -> str:
    """The code the helper's classifier names for an EPERM namespace refusal on this kernel."""
    try:
        restricted = APPARMOR_SYSCTL.read_text().strip() == '1'
    except FileNotFoundError:
        restricted = False
    return 'user_namespaces_denied_apparmor' if restricted else 'user_namespaces_denied'


def parse(output: bytes, exit_code: int, outcome: str, digest: str, refusal: str) -> dict:
    """Classifies one suite run from go test's -test.v markers and the suite's own record."""
    if len(output) > LIMIT:
        return {'status': 'failed', 'reason': 'installed-helper-suite-output-exceeded'}
    lines = output.decode('utf-8', 'replace').splitlines()
    if f'=== RUN   {TEST}' not in lines:
        return {'status': 'unverified', 'reason': 'installed-helper-suite-not-started', 'exit_code': exit_code}
    records = [line.split(RECORD, 1)[1] for line in lines if RECORD in line]
    if exit_code != 0 or f'--- PASS: {TEST}' not in {line.split(' (', 1)[0] for line in lines}:
        return {'status': 'failed', 'reason': 'installed-helper-suite-failed', 'exit_code': exit_code}
    if len(records) != 1:
        return {'status': 'failed', 'reason': 'installed-helper-record-missing'}
    try:
        record = json.loads(records[0])
    except json.JSONDecodeError:
        return {'status': 'failed', 'reason': 'installed-helper-record-malformed'}
    if (not isinstance(record, dict) or record.get('schema_version') != 1 or record.get('outcome') != outcome
            or record.get('binary_sha256') != digest or record.get('refusal_code') != refusal):
        return {'status': 'failed', 'reason': 'installed-helper-record-mismatch', 'record': record}
    return {'status': 'passed', 'record': record}


def environment(helper: Path, digest: str, outcome: str, refusal: str) -> dict:
    values = {'PATH': '/usr/bin:/bin', 'HOME': str(Path.home()), 'LC_ALL': 'C',
              'PW_INSTALLED_CONFINEMENT_HELPER': str(helper), 'PW_INSTALLED_CONFINEMENT_SHA256': digest,
              'PW_INSTALLED_CONFINEMENT_OUTCOME': outcome}
    if refusal:
        values['PW_INSTALLED_CONFINEMENT_REFUSAL'] = refusal
    return values


def execute(command: list[str], env: dict, timeout: int = 300) -> tuple[bytes, int]:
    result = subprocess.run(command, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, timeout=timeout, check=False)
    return result.stdout, result.returncode


def helper_suite(binding: dict) -> tuple[str, dict, dict]:
    if not (SUITE / 'selected_linux_test.go').is_file():
        return 'unverified', {'reason': 'installed-helper-suite-absent', 'suite': str(SUITE.relative_to(ROOT))}, {}
    go = shutil.which('go')
    if go is None:
        return 'unverified', {'reason': 'go-toolchain-absent'}, {}
    helper = unchanged(binding, 'pw')
    digest = binding['images'][str(helper)]['sha256']
    with tempfile.TemporaryDirectory(prefix='paintedwolf-helper-suite-') as directory:
        program = Path(directory) / 'confinement.test'
        built = subprocess.run([go, 'test', '-c', '-tags', 'installedhelper', '-o', str(program), './test/installed/confinement'],
                               cwd=ROOT / 'lycaon', stdin=subprocess.DEVNULL, capture_output=True, timeout=600, check=False,
                               env={**os.environ, 'GOFLAGS': '-mod=readonly', 'CGO_ENABLED': '0'})
        if built.returncode or not program.is_file():
            return 'unverified', {'reason': 'installed-helper-suite-build-unavailable'}, {'build': (built.stdout + built.stderr)[-LIMIT:]}
        arguments = [str(program), '-test.run', f'^{TEST}$', '-test.count=1', '-test.v']
        output, code = execute(arguments, environment(helper, digest, 'applied', ''))
        applied = parse(output, code, 'applied', digest, '')
        refusal = expected_refusal()
        runtime = Path(f'/run/user/{os.getuid()}')
        unit = ['systemd-run', '--user', '--wait', '--pipe', '--quiet', '--collect', '-p', 'RestrictNamespaces=yes',
                *[f'--setenv={key}={value}' for key, value in environment(helper, digest, 'refused', refusal).items()], *arguments]
        restricted, restricted_code = execute(unit, {'PATH': '/usr/bin:/bin', 'HOME': str(Path.home()),
                                                     'XDG_RUNTIME_DIR': str(runtime),
                                                     'DBUS_SESSION_BUS_ADDRESS': f'unix:path={runtime}/bus'})
        refused = parse(restricted, restricted_code, 'refused', digest, refusal)
    statuses = {applied['status'], refused['status']}
    status = 'failed' if 'failed' in statuses else 'unverified' if 'unverified' in statuses else 'passed'
    return status, {'helper': str(helper), 'helper_sha256': digest, 'applied': applied,
                    'missing_capability': {**refused, 'expected_refusal': refusal, 'restriction': 'RestrictNamespaces=yes'}}, {
        'applied-output': output[-LIMIT:], 'missing-capability-output': restricted[-LIMIT:]}
