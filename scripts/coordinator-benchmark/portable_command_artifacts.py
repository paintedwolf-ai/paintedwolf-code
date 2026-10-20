"""Independent retained-tree and behavior oracles; never trust candidate assertions."""
import bz2
import io
import pathlib
import tarfile
import tempfile
from artifacts import project_hashes
from fixture_contracts import grade as artifact_grade
import focused_outcomes as oracle
from outcomes import successful_tests
from portable_command_contracts import bytes_value, fixture_path


def grade_artifacts(project, case):
    project, expected = pathlib.Path(project), case['expected']
    files = dict(expected['files'])
    for generator in case['setup'].get('generators', []):
        for index in range(generator['count']):
            files[f"{generator['directory']}/{index:05}.txt"] = generator['body']
    names = set(files) | set(expected.get('artifact_formats', {})) | set(expected.get('behavior_files', []))
    try:
        if set(project_hashes(project)) != names:
            return False
    except ValueError:
        return False
    with tempfile.TemporaryDirectory(prefix='portable-command-oracle-') as temporary:
        baseline = pathlib.Path(temporary)
        for name, body in files.items():
            target = baseline.joinpath(*fixture_path(name).parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(bytes_value(body))
        contract = {'preserve': [], 'artifacts': {}, 'probes': [],
                    'absent': expected['absent'], 'copies': {name: name for name in files}}
        passed = artifact_grade(project, baseline, None, contract=contract)['passed']
        if behavior := expected.get('behavior_contract'):
            passed = passed and artifact_grade(project, baseline, None, contract=behavior)['passed']
    return passed and all(grade_archive(project / name, spec)
                          for name, spec in expected.get('artifact_formats', {}).items())


def grade_archive(path, spec):
    if spec['codec'] != 'bzip2':
        raise ValueError('unknown independent archive oracle')
    try:
        if path.stat().st_size > 50 * 1024 * 1024:
            return False
        with bz2.open(path, 'rb') as compressed:
            decoded = compressed.read(50 * 1024 * 1024 + 1)
        if len(decoded) > 50 * 1024 * 1024:
            return False
        seen = set()
        with tarfile.open(fileobj=io.BytesIO(decoded), mode='r:') as archive:
            for member in archive:
                if (not member.isfile() or member.name in seen
                        or member.name not in spec['tar_entries']):
                    return False
                wanted = bytes_value(spec['tar_entries'][member.name])
                if member.size != len(wanted) or archive.extractfile(member).read(len(wanted) + 1) != wanted:
                    return False
                seen.add(member.name)
        return seen == set(spec['tar_entries'])
    except (OSError, ValueError, EOFError, tarfile.TarError):
        return False


def delivered_suite(project):
    """Reuse the trusted candidate-test driver inside the existing isolated oracle."""
    driver = pathlib.Path(__file__).with_name('candidate_tests.py').read_text()
    definitions = driver.split("if __name__ == '__main__':", 1)[0]
    program = definitions + '\nprint(json.dumps(execute(pathlib.Path("/candidate"), "test*.py")))\n'
    return successful_tests(oracle.execute(pathlib.Path(project), program))
