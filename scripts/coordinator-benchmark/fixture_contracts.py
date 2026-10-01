"""Hidden fixture contracts. Only probe inputs enter the candidate runtime."""
import json
import pathlib
import tempfile
import shutil
import focused_outcomes as oracle

CONTRACTS = json.loads(pathlib.Path(__file__).with_name('fixture-contracts.json').read_text())


def context_value(value, context):
    if isinstance(value, dict):
        if set(value) == {'$context'}:
            return context[value['$context']]
        return {k: context_value(v, context) for k, v in value.items()}
    if isinstance(value, list):
        return [context_value(v, context) for v in value]
    return value


def receipt_checks(project, case, receipt):
    contract = CONTRACTS[case].get('receipt')
    if not contract:
        return []
    expected = context_value(contract['expected'], {'receipt': receipt})
    return [{'id': 'receipt-handoff', 'passed': oracle.same(oracle.read_json(project / contract['file']), expected)}]


def outside_checks(case, retained):
    """Grade the retained write-root artifact."""
    contract = CONTRACTS[case].get('outside')
    if not contract:
        return []
    manifest = retained.parent / 'manifest.json' if retained else None
    if not retained or not manifest.is_file():
        return [{'id': 'outside-artifact', 'passed': False}]
    hashes = json.loads(manifest.read_text())['files']
    target = retained / contract['file']
    present = contract['file'] in hashes and target.is_file()
    return [{'id': 'outside-artifact', 'passed': present and oracle.same(oracle.read_json(target), contract['expected'])}]


def grade(project, baseline, case):
    contract = CONTRACTS[case]
    checks = []
    def check(name, passed): checks.append({'id': name, 'passed': bool(passed)})
    for name in contract.get('absent', []):
        check('absent-' + name, not (project / name).exists())
    for name in contract['preserve']:
        path = project / name
        check('preserved-' + name, path.is_file() and path.read_bytes() == (baseline / name).read_bytes())
    for name, expected in contract['artifacts'].items():
        check(name, oracle.required_fields(oracle.read_json(project / name), expected))
    for name, expected in contract.get('artifact_bytes', {}).items():
        path = project / name
        check('preserved-current-' + name, path.is_file() and path.read_bytes() == expected.encode())
    for name, source in contract.get('copies', {}).items():
        path = project / name
        check('copied-' + name, path.is_file() and path.read_bytes() == (baseline / source).read_bytes())
    for probe in contract['probes']:
        actual = observe_probe(project, probe)
        check(probe['id'], oracle.same(actual, probe['expected']))
    if cli := contract.get('cli'):
        program = ('import subprocess,json\n'
                   f'p=subprocess.run(["python3","-I","-B","-c", "import sys,runpy;sys.path.insert(0,\'/candidate\');runpy.run_path({cli["file"]!r},run_name=\'__main__\')"],input={json.dumps(cli["input"])!r},text=True,capture_output=True)\n'
                   'print(json.dumps({"status":p.returncode,"value":json.loads(p.stdout)}))')
        check('cli-current-behavior', oracle.same(oracle.execute(project, program), {'status': cli['status'], 'value': cli['value']}))
    return {'passed': all(c['passed'] for c in checks), 'checks': checks}


def observe_probe(project, probe):
    if not probe.get('files'):
        return oracle.observe(project, probe['module'], probe['function'], probe['calls'])
    with tempfile.TemporaryDirectory(dir=project.parent) as temporary:
        candidate = pathlib.Path(temporary) / 'candidate'
        shutil.copytree(project, candidate)
        for name, body in probe['files'].items():
            (candidate / name).write_text(body)
        return oracle.observe(candidate, probe['module'], probe['function'], probe['calls'])
