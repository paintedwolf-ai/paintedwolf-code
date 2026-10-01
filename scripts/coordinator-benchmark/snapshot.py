"""Freeze the application and benchmark sources before building or spending tokens."""
import hashlib
import json
import pathlib
import os

ROOT = pathlib.Path(__file__).resolve().parents[2]
RUNTIME_METADATA = frozenset({'.DS_Store'})
SOURCE_METADATA = frozenset({'source.json', 'source-manifest.json', 'source-modes.json', 'product-manifest.json',
                             'target-contract.json', 'evaluation-source.json', 'application.json',
                             'evaluation.json', 'build-manifest.json'})


def metadata_file(name):
    return name in SOURCE_METADATA or any(name.startswith('.' + key) for key in SOURCE_METADATA)


def source_files(root):
    names = set()
    for directory, dirs, files in os.walk(root, followlinks=False):
        relative = pathlib.Path(directory).relative_to(root)
        dirs[:] = [name for name in dirs if name not in {'.git', '.bin', '.task', '__pycache__', 'node_modules'}
                   and str(relative / name) != 'lycaon-den/src-tauri/engine-root']
        for name in [*files, *[name for name in dirs if (pathlib.Path(directory) / name).is_symlink()]]:
            key = str(relative / name)
            if name not in RUNTIME_METADATA and not (relative == pathlib.Path('.') and metadata_file(name)):
                names.add(key)
    return names


def identity(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True).encode()).hexdigest()


def file_hash(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def source_entry(path):
    return {'link': str(path.readlink())} if path.is_symlink() else file_hash(path)


def tree_entries(root):
    entries = {}
    for path in sorted(root.rglob('*')):
        if path.name in RUNTIME_METADATA:
            continue
        if path.is_symlink():
            if not path.resolve().is_relative_to(root.resolve()) or not path.exists():
                raise ValueError('runtime link escapes its retained tree: ' + str(path))
            entries[str(path.relative_to(root))] = {'link': str(path.readlink())}
        elif path.is_file():
            entries[str(path.relative_to(root))] = {'sha256': file_hash(path), 'mode': path.stat().st_mode & 0o777}
        elif path.is_dir():
            entries[str(path.relative_to(root))] = {'mode': path.stat().st_mode & 0o777}
        else:
            raise ValueError('runtime contains a nonregular artifact: ' + str(path))
    return entries


def tree_hash(root):
    return identity(tree_entries(root))


def read_source(source):
    manifest = json.loads((source / 'source-manifest.json').read_text())
    if source_files(source) != set(manifest):
        raise ValueError('prepared source inventory changed')
    if any(source_entry(source / name) != expected for name, expected in manifest.items()):
        raise ValueError('prepared source changed')
    modes = json.loads((source / 'source-modes.json').read_text())
    if modes != {name: (source / name).lstat().st_mode & 0o777 for name in manifest}:
        raise ValueError('prepared source permissions changed')
    application = json.loads((source / 'source.json').read_text())
    from source_layout import verify_product
    verify_product(source, manifest, application)
    return application


def freeze(destination, selection):
    from source_layout import freeze_sources
    return freeze_sources(ROOT, destination, selection)
