"""Seal and verify the application artifacts used by a paid execution."""
import json
from snapshot import file_hash, identity, read_source, tree_entries
from progress import save

RUNTIME = 'lycaon-den/src-tauri/engine-root'
EXECUTABLES = ('.bin/lycaon-dev', '.bin/lycaon-debug', '.bin/pw-logs')


def build_manifest(source):
    runtime = source / RUNTIME
    if runtime.is_symlink() or not runtime.is_dir():
        raise ValueError('built runtime requires a regular directory')
    executables = {}
    for name in EXECUTABLES:
        path = source / name
        if path.is_symlink() or not path.is_file():
            raise ValueError('built executable requires a regular file: ' + name)
        executables[name] = {'sha256': file_hash(path), 'mode': path.stat().st_mode & 0o777}
    return {'runtime': tree_entries(runtime), 'executables': executables}


def application_identity(provenance, manifest):
    return {**provenance, 'runtime_sha256': identity(manifest['runtime'])}


def evaluation_identity(source, manifest):
    provenance = json.loads((source / 'evaluation-source.json').read_text())
    return {**provenance, 'profile': 'development-harness', 'build_sha256': identity(manifest),
            'engine_sha256': manifest['executables']['.bin/lycaon-dev']['sha256'],
            'driver_sha256': manifest['executables']['.bin/lycaon-debug']['sha256']}


def require_unadmitted(source):
    if any((source.parent / name).exists() for name in ('plan.json', 'captures', 'provider-preflight')):
        raise ValueError('cannot seal application artifacts after execution admission')


def seal_application(source):
    if (source / 'application.json').exists():
        return verify_application(source)
    require_unadmitted(source)
    provenance = read_source(source)
    manifest = build_manifest(source)
    application = application_identity(provenance, manifest)
    save(source / 'build-manifest.json', manifest)
    save(source / 'evaluation.json', evaluation_identity(source, manifest))
    save(source / 'application.json', application)
    return verify_application(source)


def verify_application(source, models=None, *, plan=None):
    provenance = read_source(source)
    manifest = json.loads((source / 'build-manifest.json').read_text())
    application = json.loads((source / 'application.json').read_text())
    if application != application_identity(provenance, manifest):
        raise ValueError('built application identity changed')
    if build_manifest(source) != manifest:
        raise ValueError('sealed application artifacts changed')
    if json.loads((source / 'evaluation.json').read_text()) != evaluation_identity(source, manifest):
        raise ValueError('sealed evaluation identity changed')
    if models is not None and any(model['configuration'].get('application') != application for model in models):
        raise ValueError('built application differs from the admitted plan')
    if plan is not None:
        if (plan['benchmark']['evaluation'] != evaluation_identity(source, manifest)
                or plan['benchmark']['application_version'] != application['version']):
            raise ValueError('admitted evaluation differs from its sealed artifacts')
        if plan['mode'] == 'release' and application['target']['kind'] not in {'tag', 'commit'}:
            raise ValueError('release scores require an immutable application target')
        lineage = plan['lineage']
        if lineage is not None and lineage['application_source_sha256'] != application['source_sha256']:
            raise ValueError('repair lineage differs from the retained application')
    return application
