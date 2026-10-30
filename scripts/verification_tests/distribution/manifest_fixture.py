"""One portable producer corpus for the native manifest readers."""
import importlib.util
import json
from pathlib import Path
import shutil
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parents[3]
FIXTURE = ROOT / 'packaging/linux/manifest/testdata'
SPEC = importlib.util.spec_from_file_location('contract_manifest_producer', ROOT / 'scripts/linux-package-manifest.py')
PRODUCER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PRODUCER)
ARCHITECTURES = {'x86_64': 'x86_64-unknown-linux-gnu', 'aarch64': 'aarch64-unknown-linux-gnu'}


def generate(base, architecture):
    """Run generate, seal and verify, retaining the real producer's exact bytes."""
    source, stage = base / 'source', base / 'stage'
    target = ARCHITECTURES[architecture]
    tauri = source / 'lycaon-den/src-tauri'
    elf = (FIXTURE / (architecture + '.elf')).read_bytes()
    for name in ['painted-wolf-code', *PRODUCER.HELPERS, PRODUCER.BOOTSTRAP['name']]:
        path = (tauri / 'target' / target / 'release' / name if name == 'painted-wolf-code'
                else tauri / 'binaries' / (name + '-' + target))
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(elf)
        path.chmod(0o755)
    for name in PRODUCER.ENTRY_POINTS:
        path = source / 'packaging/linux/bin' / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes((FIXTURE / 'launcher.sh').read_bytes())
        path.chmod(0o755)
    resources = tauri / 'engine-root/resources'
    resources.mkdir(parents=True)
    (resources / 'settings.json').write_bytes((FIXTURE / 'payload/engine-root/resources/settings.json').read_bytes())
    (resources / 'current').symlink_to('settings.json')
    (source / 'VERSION').write_bytes((FIXTURE / 'payload/release-version').read_bytes())
    (tauri / 'THIRD-PARTY-NOTICES.md').write_bytes((FIXTURE / 'payload/THIRD-PARTY-NOTICES.md').read_bytes())
    launch_catalog = source / 'packaging/linux' / PRODUCER.LAUNCH_CATALOG
    shutil.copyfile(ROOT / 'packaging/linux' / PRODUCER.LAUNCH_CATALOG, launch_catalog)
    manifest = base / 'generated.json'
    PRODUCER.generate(SimpleNamespace(root=source, target=target, version='1.2.3', output=manifest))
    install = stage / PRODUCER.INSTALL_ROOT.lstrip('/')
    shutil.copytree(tauri / 'engine-root', install / 'engine-root', symlinks=True)
    for name in ['painted-wolf-code', *PRODUCER.HELPERS]:
        path = install / 'bin' / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(elf)
        path.chmod(0o755)
    bootstrap = stage / PRODUCER.BOOTSTRAP['path'].lstrip('/')
    bootstrap.parent.mkdir(parents=True, exist_ok=True)
    bootstrap.write_bytes(elf)
    bootstrap.chmod(0o755)
    for name in PRODUCER.ENTRY_POINTS:
        path = stage / 'usr/bin' / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes((FIXTURE / 'launcher.sh').read_bytes())
        path.chmod(0o755)
    shutil.copyfile(source / 'VERSION', install / 'release-version')
    shutil.copyfile(tauri / 'THIRD-PARTY-NOTICES.md', install / 'THIRD-PARTY-NOTICES.md')
    shutil.copyfile(launch_catalog, install / PRODUCER.LAUNCH_CATALOG)
    shutil.copyfile(manifest, install / 'manifest.json')
    for path in stage.rglob('*'):
        if path.is_dir() and not path.is_symlink():
            path.chmod(0o755)
        elif path.is_file() and not path.is_symlink() and not path.stat().st_mode & 0o111:
            path.chmod(0o644)
    args = SimpleNamespace(tree=stage, version='1.2.3', architecture=architecture)
    PRODUCER.seal_tree(args)
    PRODUCER.verify_tree(args)
    return (install / 'manifest.json').read_bytes()


def relocate(document, root):
    """Only physical roots change; shape, architecture and all sealed fields survive."""
    document = json.loads(json.dumps(document))
    previous = document['install_root']
    document['install_root'] = str(root)
    for row in document['executables']:
        if row['path'].startswith(previous + '/'):
            row['path'] = str(root) + row['path'][len(previous):]
    return document
