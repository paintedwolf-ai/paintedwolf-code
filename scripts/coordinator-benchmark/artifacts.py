"""Keep independently graded project files with their execution evidence."""
import json
import pathlib
import shutil
import tempfile
from snapshot import file_hash


class InvalidCandidate(ValueError):
    """The delivered tree violates the declared artifact contract."""


def copy_project(source, destination):
    if source.is_symlink():
        raise InvalidCandidate('candidate directory is a symlink')
    if not source.is_dir():
        raise ValueError('completed project is unavailable')
    destination.mkdir()
    for path in source.rglob('*'):
        relative = path.relative_to(source)
        if any(p in {'.paintedwolf', '.git', '__pycache__'} for p in relative.parts):
            continue
        if path.is_symlink():
            raise InvalidCandidate('candidate contains a symlink: ' + str(relative))
        target = destination / relative
        if path.is_dir():
            target.mkdir(exist_ok=True)
        elif path.is_file():
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)
            target.chmod(0o644)
        else:
            raise InvalidCandidate('candidate contains a non-regular file: ' + str(relative))


def project_hashes(project):
    result = {}
    for path in project.rglob('*'):
        if path.is_symlink():
            raise ValueError('retained artifact contains a symlink')
        if path.is_file():
            result[path.relative_to(project).as_posix()] = file_hash(path)
        elif not path.is_dir():
            raise ValueError('retained artifact contains a non-regular file')
    return result


def retain_tree(source, destination, identity):
    if destination.is_symlink():
        raise ValueError('retained artifact directory is a symlink')
    if not destination.exists():
        with tempfile.TemporaryDirectory(prefix='.artifact-', dir=destination.parent) as temporary:
            staged = pathlib.Path(temporary)
            copy_project(source, staged / 'files')
            metadata = {**identity, 'files': project_hashes(staged / 'files')}
            (staged / 'manifest.json').write_text(json.dumps(metadata, indent=2) + '\n')
            staged.rename(destination)
    metadata = json.loads((destination / 'manifest.json').read_text())
    if any(metadata.get(key) != value for key, value in identity.items()):
        raise ValueError('retained artifact belongs to another execution')
    files = destination / 'files'
    if files.is_symlink() or not files.is_dir():
        raise ValueError('retained artifact files are unavailable')
    if metadata['files'] != project_hashes(files):
        raise ValueError('retained artifact changed after capture')
    return files


def retain_project(capture, case):
    return retain_tree(pathlib.Path(case['project_dir']), capture / 'graded-project',
                       {'session_id': case['session_id'], 'source_project': case['project_dir']})


def retain_outside(capture, case):
    source_path = (case.get('sandbox') or {}).get('path')
    if not source_path:
        return None
    source = pathlib.Path(source_path)
    destination = capture / 'graded-outside'
    if not destination.exists() and not source.is_dir():
        return None
    return retain_tree(source, destination,
                       {'session_id': case['session_id'], 'source_path': str(source)})
