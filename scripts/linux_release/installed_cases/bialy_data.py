"""Retain readonly decision data after independent final-payload verification."""
from __future__ import annotations

from pathlib import Path
import stat

MAX_BYTES = 1 << 30
MAX_FILES = 64


def physical(path: Path, *, directory: bool):
    for ancestor in [*reversed(path.parents), path]:
        info = ancestor.lstat()
        if (info.st_uid != 0 or info.st_mode & 0o022 or stat.S_ISLNK(info.st_mode)
                or ancestor != path and not stat.S_ISDIR(info.st_mode)):
            raise ValueError('installed decision data requires physical root-owned readonly ancestry')
    info = path.lstat()
    if directory != stat.S_ISDIR(info.st_mode) or not directory and not stat.S_ISREG(info.st_mode):
        raise ValueError('installed decision data has an unsupported file type')
    return info


def capture(root: Path) -> dict | None:
    # Called only after the selected package or recursive NAR has independently verified all bytes.
    from ..installed_runtime import file_identity
    decision = root / 'engine-root/decide'
    if not decision.exists():
        return None
    physical(decision, directory=True)
    models = decision / 'models'
    physical(models, directory=True)
    checkpoints = list(models.iterdir())
    if len(checkpoints) != 1:
        raise ValueError('final installed decision data must select one shipped checkpoint')
    model = checkpoints[0]
    physical(model, directory=True)
    pending, files, total = [decision], {}, 0
    directories = 0
    while pending:
        directory = pending.pop()
        physical(directory, directory=True)
        directories += 1
        if directories > MAX_FILES:
            raise ValueError('installed decision data exceeds its directory bound')
        for path in directory.iterdir():
            info = path.lstat()
            if stat.S_ISDIR(info.st_mode):
                pending.append(path)
                continue
            physical(path, directory=False)
            total += info.st_size
            if len(files) >= MAX_FILES or total > MAX_BYTES or info.st_mode & 0o111:
                raise ValueError('installed decision data exceeds its file or byte bounds')
            identity = file_identity(path)
            identity.pop('header')
            files[str(path.relative_to(decision))] = identity
    required = {'rl_agent_config.json', 'model.safetensors', 'encoder/config.json',
                'tokenizer/tokenizer.json', 'tokenizer/tokenizer_config.json'}
    if not {str(model.relative_to(decision) / name) for name in required} <= files.keys():
        raise ValueError('installed decision checkpoint is incomplete')
    return {'model': str(model), 'root': str(decision), 'files': files, 'bytes': total}


def unchanged(binding: dict) -> dict:
    expected = binding.get('bialy_data')
    if expected is None:
        from .records import CaseUnavailable
        raise CaseUnavailable('final installed binding has no verified Bialy data receipt')
    observed = capture(Path(binding['install_root']))
    if observed != expected:
        raise ValueError('installed Bialy data changed after final-payload binding')
    return observed
