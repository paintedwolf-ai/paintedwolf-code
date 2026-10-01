"""Compose evaluation sources inside the boundary declared by the application target."""
import fnmatch
import json
import pathlib
import shutil
import tempfile

import source_revision
from snapshot import identity, source_entry, read_source
from progress import save


def owns(name, paths):
    return any(name.startswith(pattern) if pattern.endswith('/') else matches_file(name, pattern)
               for pattern in paths)


def matches_file(name, pattern):
    parts, patterns = name.split('/'), pattern.split('/')
    return len(parts) == len(patterns) and all(fnmatch.fnmatchcase(part, glob)
                                              for part, glob in zip(parts, patterns))


def product_entries(manifest, contract):
    paths = contract['benchmark_paths'] + contract['harness_paths']
    return {name: entry for name, entry in manifest.items() if not owns(name, paths)}


def entries(root, names):
    result = {}
    for name in sorted(names):
        path = root / name
        if path.is_symlink() and not name.startswith('lycaon/test/fixtures/'):
            raise ValueError('executable source requires regular files: ' + name)
        result[name] = source_entry(path)
    return result


def overlay(destination, manifest, incoming, names, paths):
    for name in list(manifest):
        if owns(name, paths):
            (destination / name).unlink()
            del manifest[name]
    for name in names:
        if owns(name, paths):
            target = destination / name
            target.parent.mkdir(parents=True, exist_ok=True)
            if any(parent.is_symlink() for parent in target.parents if parent.is_relative_to(destination)):
                raise ValueError('evaluation source crosses a symlink')
            shutil.copy2(incoming / name, target, follow_symlinks=False)
            manifest[name] = source_entry(target)


def freeze_sources(repository, destination, selection):
    target, benchmark = selection['application'], selection['benchmark']
    harness = selection.get('harness') or target
    retained = selection.get('retained_source')
    if retained:
        retained = pathlib.Path(retained)
        original = read_source(retained)
        if original['target'] != target:
            raise ValueError('repair target differs from retained application')
        contract = json.loads((retained / 'target-contract.json').read_text())
        manifest = json.loads((retained / 'source-manifest.json').read_text())
        destination.mkdir(parents=True, exist_ok=False)
        for name in manifest:
            path = destination / name
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(retained / name, path, follow_symlinks=False)
    else:
        contract = source_revision.contract(repository, target)
        names = source_revision.export(repository, target, destination)
        manifest = entries(destination, names)
    for revision in (benchmark, *([harness] if not retained or selection.get('replace_harness') else [])):
        if source_revision.contract(repository, revision) != contract:
            raise ValueError('selected benchmark or harness does not support the target application contract')
    product = product_entries(manifest, contract)
    replacements = [(benchmark, contract['benchmark_paths'])]
    if not retained or selection.get('replace_harness'):
        replacements.append((harness, contract['harness_paths']))
    for revision, paths in replacements:
        with tempfile.TemporaryDirectory() as temporary:
            incoming = pathlib.Path(temporary) / 'source'
            names = source_revision.export(repository, revision, incoming)
            entries(incoming, names)
            overlay(destination, manifest, incoming, names, paths)
    if product_entries(manifest, contract) != product:
        raise ValueError('evaluation changed application product source')
    benchmark_manifest = json.loads((destination / 'scripts/coordinator-benchmark/benchmark.json').read_text())
    if type(benchmark_manifest.get('revision')) is not int or benchmark_manifest['revision'] < 1:
        raise ValueError('benchmark requires a positive revision')
    if (type(benchmark_manifest.get('application_contract')) is not int
            or benchmark_manifest['application_contract'] != contract['revision']):
        raise ValueError('benchmark does not support this application evaluation contract')
    application = {'version': (destination / 'VERSION').read_text().strip(),
                   'revision': target['revision'], 'dirty': target['dirty'],
                   'source_sha256': identity(product), 'target': target,
                   'contract_sha256': identity(contract)}
    evaluation = {'contract': contract['revision'], 'benchmark_source': benchmark, 'harness_source': harness,
                  'benchmark_sha256': identity({k:v for k,v in manifest.items() if owns(k, contract['benchmark_paths'])}),
                  'harness_sha256': identity({k:v for k,v in manifest.items() if owns(k, contract['harness_paths'])})}
    save(destination / 'target-contract.json', contract)
    save(destination / 'product-manifest.json', product)
    save(destination / 'evaluation-source.json', evaluation)
    save(destination / 'source-manifest.json', manifest)
    save(destination / 'source-modes.json', {name: (destination / name).lstat().st_mode & 0o777 for name in manifest})
    save(destination / 'source.json', application)
    return application


def verify_product(source, manifest, application):
    contract = json.loads((source / 'target-contract.json').read_text())
    product = json.loads((source / 'product-manifest.json').read_text())
    if identity(contract) != application['contract_sha256'] or product_entries(manifest, contract) != product:
        raise ValueError('application evaluation boundary changed')
    if identity(product) != application['source_sha256']:
        raise ValueError('application product source identity changed')
    evaluation = json.loads((source / 'evaluation-source.json').read_text())
    for key, group in [('benchmark_sha256', 'benchmark_paths'), ('harness_sha256', 'harness_paths')]:
        if evaluation[key] != identity({k:v for k,v in manifest.items() if owns(k, contract[group])}):
            raise ValueError('evaluation source identity changed')
